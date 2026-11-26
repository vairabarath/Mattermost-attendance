package attendance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// WFH errors.
var (
	ErrWFHOverlap     = errors.New("overlaps an existing WFH request")
	ErrAlreadyDecided = errors.New("already decided")
)

// WFH request statuses.
const (
	WFHPending   = "pending"
	WFHApproved  = "approved"
	WFHRejected  = "rejected"
	WFHCancelled = "cancelled"
)

// WFHRequest is one work-from-home request. Dates are YYYY-MM-DD.
type WFHRequest struct {
	ID            int64
	UserID        string
	Username      string
	TeamID        string
	Start, End    string
	HalfDay       bool
	Reason        string
	Status        string
	DecidedBy     string
	DecidedByName string
	DecidedAt     *time.Time
	Note          string
	AdminPostID   string
	CreatedAt     time.Time
}

// WFHStore is the persistence for WFH requests.
type WFHStore interface {
	// CreateWFH returns ErrWFHOverlap if it overlaps the member's pending or
	// approved requests in the same team.
	CreateWFH(ctx context.Context, r WFHRequest) (int64, error)
	GetWFH(ctx context.Context, id int64) (*WFHRequest, error)
	SetWFHAdminPost(ctx context.Context, id int64, postID string) error
	ListWFH(ctx context.Context, userID, teamID string, limit int) ([]WFHRequest, error)
	// ListTeamWFH lists the team's requests (any status) ending on or after fromDay.
	ListTeamWFH(ctx context.Context, teamID, fromDay string, limit int) ([]WFHRequest, error)
	// PendingWFH lists the team's pending requests; coveringDay ("" = any)
	// keeps only those that include that day.
	PendingWFH(ctx context.Context, teamID, coveringDay string) ([]WFHRequest, error)
	// DecideWFH moves a pending request to status; if it isn't pending any
	// more it returns the current request with ErrAlreadyDecided.
	DecideWFH(ctx context.Context, id int64, teamID, status, deciderID, note string, at time.Time) (*WFHRequest, error)
	// CancelWFH cancels the member's pending request, or an approved one
	// starting after today. Returns nil if nothing was cancelled.
	CancelWFH(ctx context.Context, id int64, userID, today string) (*WFHRequest, error)
	// WFHApprovedOn reports an approved FULL-day request covering day
	// (half days are informational only).
	WFHApprovedOn(ctx context.Context, userID, teamID, day string) (bool, error)
	// ApprovedWFHDays counts approved WFH days in [fromDay, toDay] (½ for half days).
	ApprovedWFHDays(ctx context.Context, userID, teamID, fromDay, toDay string) (float64, error)
	// SetOpenSessionMode sets the mode of the member's open session in teamID.
	SetOpenSessionMode(ctx context.Context, userID, teamID, mode string) (bool, error)
}

// Poster posts and edits bot messages.
type Poster interface {
	CreatePostWithButtons(ctx context.Context, channelID, message string, atts []mattermost.Attachment) (string, error)
	UpdatePost(ctx context.Context, postID, message string, atts []mattermost.Attachment) error
	EphemeralPost(ctx context.Context, userID, channelID, message string) error
	CreateReply(ctx context.Context, channelID, rootID, message string) (string, error)
	// Permalink is a post's web link (for alerts).
	Permalink(ctx context.Context, teamID, postID string) (string, error)
	DirectMessage(ctx context.Context, userID, message string) error
	OpenDialog(ctx context.Context, triggerID, submitURL string, d mattermost.Dialog) error
}

// Click is a verified button click.
type Click struct {
	Action    string
	UserID    string            // who clicked
	Fields    map[string]string // verified context
	TriggerID string
}

// DialogSubmit is a verified dialog submission.
type DialogSubmit struct {
	Fields     map[string]string // verified state
	UserID     string
	ChannelID  string
	Submission map[string]any
}

// DialogResult answers a dialog submission.
type DialogResult struct {
	Errors map[string]string // keeps the dialog open with field errors
	Error  string
}

// Button / dialog kinds for WFH.
const (
	ActWFHApprove = "wfh_approve"
	ActWFHReject  = "wfh_reject"
	dlgWFH        = "wfh"
	dlgReject     = "wfh_reject"
	maxWFHDays    = 31
	// Admins may decide days later; the pending-status check guards reuse.
	wfhButtonTTL = 60 * 24 * time.Hour
)

// --- validation (pure) ---

// ValidateWFH checks the form values. Field errors are keyed by element name.
func ValidateWFH(start, end, reason string, now time.Time, loc *time.Location) (time.Time, time.Time, map[string]string) {
	errs := map[string]string{}
	today, _ := DayBounds(now, loc)
	s, err := time.ParseInLocation(DayFormat, strings.TrimSpace(start), loc)
	if err != nil {
		errs["start_date"] = "Use YYYY-MM-DD, e.g. " + now.In(loc).AddDate(0, 0, 1).Format(DayFormat)
	} else if s.Before(today) {
		errs["start_date"] = "This date is in the past."
	}
	e := s
	if strings.TrimSpace(end) != "" {
		if e, err = time.ParseInLocation(DayFormat, strings.TrimSpace(end), loc); err != nil {
			errs["end_date"] = "Use YYYY-MM-DD, or leave empty for a single day."
		}
	}
	if _, bad := errs["start_date"]; !bad && errs["end_date"] == "" {
		switch {
		case e.Before(s):
			errs["end_date"] = "The end date is before the start date."
		case daysBetween(s, e) >= maxWFHDays:
			errs["end_date"] = fmt.Sprintf("At most %d days per request.", maxWFHDays)
		}
	}
	if len([]rune(strings.TrimSpace(reason))) < 3 {
		errs["reason"] = "Please give a reason."
	}
	return s, e, errs
}

func daysBetween(a, b time.Time) int {
	return int(b.Sub(a).Hours()/24 + 0.5)
}

// IsUrgent: the request is for today, or for tomorrow and made after 20:00.
func IsUrgent(start time.Time, now time.Time, loc *time.Location) bool {
	local := now.In(loc)
	today := local.Format(DayFormat)
	tomorrow := local.AddDate(0, 0, 1).Format(DayFormat)
	day := start.In(loc).Format(DayFormat)
	return day == today || (day == tomorrow && local.Hour() >= 20)
}

// --- formatting ---

func (s *Service) wfhDates(r WFHRequest) string { return wfhDatesIn(r, s.Loc) }

func wfhDatesIn(r WFHRequest, loc *time.Location) string {
	st, _ := time.ParseInLocation(DayFormat, r.Start, loc)
	en, _ := time.ParseInLocation(DayFormat, r.End, loc)
	out := st.Format("Mon 2 Jan")
	if r.End != r.Start {
		out += " – " + en.Format("Mon 2 Jan") + fmt.Sprintf(" (%d days)", daysBetween(st, en)+1)
	}
	if r.HalfDay {
		out += " (½ day)"
	} else if r.End == r.Start {
		out += " (full day)"
	}
	return out
}

func statusIcon(st string) string {
	switch st {
	case WFHApproved:
		return "✅"
	case WFHRejected:
		return "❌"
	case WFHCancelled:
		return "🚫"
	}
	return "⏳"
}

// adminPostText is the #attendance-admin message for a request.
func (s *Service) adminPostText(r WFHRequest, used float64, urgent bool) string {
	var b strings.Builder
	if urgent && r.Status == WFHPending {
		b.WriteString("⚡ **Urgent** · ")
	}
	fmt.Fprintf(&b, "🏠 **WFH request #%d** — @%s — %s\n**Reason:** %s\nWFH days approved this month: %s",
		r.ID, r.Username, s.wfhDates(r), r.Reason, fmtDays(used))
	switch r.Status {
	case WFHApproved:
		fmt.Fprintf(&b, "\n\n✅ **Approved** by @%s at %s", r.DecidedByName, s.fmtDecided(r))
	case WFHRejected:
		fmt.Fprintf(&b, "\n\n❌ **Rejected** by @%s at %s — %s", r.DecidedByName, s.fmtDecided(r), r.Note)
	case WFHCancelled:
		b.WriteString("\n\n🚫 **Cancelled by member**")
	}
	return b.String()
}

func (s *Service) fmtDecided(r WFHRequest) string {
	if r.DecidedAt == nil {
		return ""
	}
	return s.fmtSince(*r.DecidedAt, s.Now())
}

func fmtDays(d float64) string {
	return strconv.FormatFloat(d, 'f', -1, 64)
}

// --- /att wfh ---

const wfhUsage = "Usage:\n" +
	"- `/att wfh` — request work from home (opens a form)\n" +
	"- `/att wfh list` — your requests in this team\n" +
	"- `/att wfh cancel <id>` — cancel a pending request, or an approved one that hasn't started\n" +
	"- `/att wfh pending` — pending requests for this team (admins)\n" +
	"- `/att wfh all` — every request in this team from the last 30 days and upcoming (admins)"

func (s *Service) wfh(ctx context.Context, c Command, args string) (Reply, error) {
	sub, rest := splitFirst(args)
	switch sub {
	case "":
		return s.wfhOpenForm(ctx, c)
	case "list":
		return s.wfhList(ctx, c)
	case "cancel":
		return s.wfhCancel(ctx, c, rest)
	case "pending":
		return s.wfhPending(ctx, c)
	case "all":
		return s.wfhAll(ctx, c)
	}
	return text(wfhUsage), nil
}

// signedState encodes signed fields as a dialog state string.
func (s *Service) signedState(fields map[string]string) string {
	b, _ := json.Marshal(s.Signer.Sign(fields))
	return string(b)
}

func (s *Service) wfhOpenForm(ctx context.Context, c Command) (Reply, error) {
	if c.TriggerID == "" || s.Posts == nil {
		return text("Couldn't open the form. Please try `/att wfh` again."), nil
	}
	tomorrow := s.Now().In(s.Loc).AddDate(0, 0, 1).Format(DayFormat)
	d := mattermost.Dialog{
		CallbackID:       dlgWFH,
		Title:            "Work from home request",
		IntroductionText: "For **" + s.Teams.TeamName(c.TeamID) + "**. Admins of this team will approve or reject it.",
		SubmitLabel:      "Request",
		State:            s.signedState(map[string]string{"k": dlgWFH, "u": c.UserID, "t": c.TeamID}),
		Elements: []mattermost.DialogElement{
			{DisplayName: "Start date", Name: "start_date", Type: "text", Placeholder: "YYYY-MM-DD", Default: tomorrow, MaxLength: 10},
			{DisplayName: "End date", Name: "end_date", Type: "text", Optional: true, Placeholder: "YYYY-MM-DD", HelpText: "Leave empty for a single day.", MaxLength: 10},
			{DisplayName: "Half day", Name: "half_day", Type: "bool", Optional: true, Placeholder: "Only half of the day"},
			{DisplayName: "Reason", Name: "reason", Type: "textarea", MaxLength: maxNoteLen},
		},
	}
	if err := s.Posts.OpenDialog(ctx, c.TriggerID, s.DialogURL, d); err != nil {
		return Reply{}, err
	}
	return Reply{}, nil // the form is the response
}

// HandleDialog processes a verified dialog submission.
func (s *Service) HandleDialog(ctx context.Context, d DialogSubmit) DialogResult {
	log := s.Log.With("user_id", d.UserID, "dialog", d.Fields["k"])
	var (
		res DialogResult
		err error
	)
	switch d.Fields["k"] {
	case dlgWFH:
		res, err = s.submitWFH(ctx, d)
	case dlgReject:
		res, err = s.submitReject(ctx, d)
	case dlgEOD:
		res, err = s.submitEOD(ctx, d)
	default:
		return DialogResult{Error: "Unknown form."}
	}
	if err != nil {
		log.Error("dialog failed", "err", err)
		return DialogResult{Error: "Something went wrong. Please try again."}
	}
	log.Info("dialog ok")
	return res
}

func strField(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func boolField(m map[string]any, k string) bool {
	switch v := m[k].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

func (s *Service) submitWFH(ctx context.Context, d DialogSubmit) (DialogResult, error) {
	teamID := d.Fields["t"]
	team, ok := s.TeamConfig(teamID)
	if !ok {
		return DialogResult{Error: "Unknown team."}, nil
	}
	now := s.Now()
	reason := truncate(strField(d.Submission, "reason"), maxNoteLen)
	start, end, errs := ValidateWFH(strField(d.Submission, "start_date"), strField(d.Submission, "end_date"), reason, now, s.Loc)
	if len(errs) > 0 {
		return DialogResult{Errors: errs}, nil
	}
	if err := s.ensureMember(ctx, d.UserID, ""); err != nil {
		return DialogResult{}, err
	}
	r := WFHRequest{
		UserID: d.UserID, TeamID: teamID,
		Start: start.Format(DayFormat), End: end.Format(DayFormat),
		HalfDay: boolField(d.Submission, "half_day"), Reason: reason, Status: WFHPending,
	}
	id, err := s.Store.CreateWFH(ctx, r)
	if errors.Is(err, ErrWFHOverlap) {
		return DialogResult{Errors: map[string]string{"start_date": "You already have a pending or approved request covering these dates in this team (`/att wfh list`)."}}, nil
	}
	if err != nil {
		return DialogResult{}, err
	}
	full, err := s.Store.GetWFH(ctx, id)
	if err != nil || full == nil {
		return DialogResult{}, err
	}

	if team.AdminChannelID != "" {
		used, err := s.usedThisMonth(ctx, *full)
		if err != nil {
			return DialogResult{}, err
		}
		postID, err := s.Posts.CreatePostWithButtons(ctx, team.AdminChannelID,
			s.adminPostText(*full, used, IsUrgent(start, now, s.Loc)), s.wfhButtons(id, teamID))
		if err != nil {
			s.Log.Error("post WFH request to admins", "wfh_id", id, "err", err)
		} else if err := s.Store.SetWFHAdminPost(ctx, id, postID); err != nil {
			s.Log.Error("save admin post id", "wfh_id", id, "err", err)
		}
	}
	msg := fmt.Sprintf("🏠 WFH request **#%d** for %s sent to the %s admins. You'll get a DM when it's decided.", id, s.wfhDates(*full), team.Name)
	if err := s.Posts.EphemeralPost(ctx, d.UserID, d.ChannelID, msg); err != nil {
		s.Log.Warn("WFH confirmation failed", "err", err)
	}
	return DialogResult{}, nil
}

func (s *Service) usedThisMonth(ctx context.Context, r WFHRequest) (float64, error) {
	st, _ := time.ParseInLocation(DayFormat, r.Start, s.Loc)
	first := time.Date(st.Year(), st.Month(), 1, 0, 0, 0, 0, s.Loc)
	last := first.AddDate(0, 1, -1)
	return s.Store.ApprovedWFHDays(ctx, r.UserID, r.TeamID, first.Format(DayFormat), last.Format(DayFormat))
}

func (s *Service) wfhButtons(id int64, teamID string) []mattermost.Attachment {
	idStr := strconv.FormatInt(id, 10)
	btn := func(bid, label, style, action string) mattermost.Action {
		return mattermost.Action{ID: bid, Name: label, Style: style, Integration: mattermost.Integration{
			URL: s.ActionURL, Context: s.Signer.SignFor(map[string]string{"a": action, "w": idStr, "t": teamID}, wfhButtonTTL),
		}}
	}
	return []mattermost.Attachment{{Actions: []mattermost.Action{
		btn("wfhapprove", "✅ Approve", "success", ActWFHApprove),
		btn("wfhreject", "❌ Reject", "danger", ActWFHReject),
	}}}
}

// handleWFHClick handles Approve / Reject clicks from #attendance-admin.
func (s *Service) handleWFHClick(ctx context.Context, c Click) (Reply, error) {
	teamID := c.Fields["t"]
	id, err := strconv.ParseInt(c.Fields["w"], 10, 64)
	if err != nil {
		return ephemeral("Invalid request."), nil
	}
	if ok, err := s.Admins.IsAdmin(ctx, teamID, c.UserID); err != nil || !ok {
		return ephemeral("🔒 Only admins of " + s.Teams.TeamName(teamID) + " can decide WFH requests."), err
	}
	r, err := s.Store.GetWFH(ctx, id)
	if err != nil {
		return Reply{}, err
	}
	if r == nil || r.TeamID != teamID {
		return ephemeral("Request not found."), nil
	}
	if r.Status != WFHPending {
		return ephemeral(fmt.Sprintf("Already %s.", r.Status)), nil
	}

	if c.Action == ActWFHReject {
		d := mattermost.Dialog{
			CallbackID: dlgReject, Title: fmt.Sprintf("Reject WFH request #%d", id),
			IntroductionText: fmt.Sprintf("@%s — %s", r.Username, s.wfhDates(*r)),
			SubmitLabel:      "Reject",
			State:            s.signedState(map[string]string{"k": dlgReject, "u": c.UserID, "t": teamID, "w": c.Fields["w"]}),
			Elements:         []mattermost.DialogElement{{DisplayName: "Reason", Name: "reason", Type: "textarea", MaxLength: maxNoteLen, HelpText: "Sent to the member."}},
		}
		if err := s.Posts.OpenDialog(ctx, c.TriggerID, s.DialogURL, d); err != nil {
			return Reply{}, err
		}
		return ephemeral(""), nil
	}

	decided, err := s.decide(ctx, id, teamID, WFHApproved, c.UserID, "")
	if errors.Is(err, ErrAlreadyDecided) {
		return ephemeral(fmt.Sprintf("Already %s by @%s.", decided.Status, decided.DecidedByName)), nil
	}
	if err != nil {
		return Reply{}, err
	}
	used, err := s.usedThisMonth(ctx, *decided)
	if err != nil {
		return Reply{}, err
	}
	// Replacing the post without attachments removes the buttons.
	return text(s.adminPostText(*decided, used, false)), nil
}

func (s *Service) submitReject(ctx context.Context, d DialogSubmit) (DialogResult, error) {
	teamID := d.Fields["t"]
	id, err := strconv.ParseInt(d.Fields["w"], 10, 64)
	if err != nil {
		return DialogResult{Error: "Invalid request."}, nil
	}
	if ok, err := s.Admins.IsAdmin(ctx, teamID, d.UserID); err != nil || !ok {
		return DialogResult{Error: "Only admins of this team can decide WFH requests."}, err
	}
	reason := truncate(strField(d.Submission, "reason"), maxNoteLen)
	if len([]rune(reason)) < 3 {
		return DialogResult{Errors: map[string]string{"reason": "Please give a reason."}}, nil
	}
	decided, err := s.decide(ctx, id, teamID, WFHRejected, d.UserID, reason)
	if errors.Is(err, ErrAlreadyDecided) {
		return DialogResult{Error: fmt.Sprintf("Already %s by @%s.", decided.Status, decided.DecidedByName)}, nil
	}
	if err != nil {
		return DialogResult{}, err
	}
	s.refreshAdminPost(ctx, *decided)
	return DialogResult{}, nil
}

// decide records the decision, applies a same-day approval to an open
// session, DMs the member and writes the audit log.
func (s *Service) decide(ctx context.Context, id int64, teamID, status, adminID, note string) (*WFHRequest, error) {
	now := s.Now()
	r, err := s.Store.DecideWFH(ctx, id, teamID, status, adminID, note, now)
	if err != nil {
		return r, err
	}
	if err := s.Store.InsertAudit(ctx, AuditEntry{
		ActorID: adminID, TargetID: r.UserID, TeamID: teamID, Action: "wfh_" + strings.TrimSuffix(status, "d"),
		Old:    map[string]any{"wfh_id": id, "status": WFHPending},
		New:    map[string]any{"wfh_id": id, "status": status, "start": r.Start, "end": r.End, "half_day": r.HalfDay},
		Reason: note,
	}); err != nil {
		s.Log.Error("WFH audit", "err", err)
	}

	today := now.In(s.Loc).Format(DayFormat)
	if status == WFHApproved && !r.HalfDay && r.Start <= today && today <= r.End {
		if changed, err := s.Store.SetOpenSessionMode(ctx, r.UserID, teamID, "wfh"); err != nil {
			s.Log.Error("set open session to wfh", "err", err)
		} else if changed {
			if open, err := s.Store.OpenSession(ctx, r.UserID); err == nil && !onBreak(open) {
				s.Status.Enqueue(r.UserID, StatusWorkingFromHome, false)
			}
		}
	}

	team := s.Teams.TeamName(teamID)
	dm := fmt.Sprintf("✅ Your WFH request **#%d** for %s in **%s** was approved by @%s.", id, s.wfhDates(*r), team, r.DecidedByName)
	if status == WFHRejected {
		dm = fmt.Sprintf("❌ Your WFH request **#%d** for %s in **%s** was rejected by @%s: %s", id, s.wfhDates(*r), team, r.DecidedByName, note)
	} else if r.HalfDay {
		dm += " Half days are noted for information; check in as usual."
	} else {
		dm += " Check in with `/att in` as usual — it'll be recorded as 🏠."
	}
	if err := s.Posts.DirectMessage(ctx, r.UserID, dm); err != nil {
		s.Log.Warn("WFH decision DM failed", "user_id", r.UserID, "err", err)
	}
	return r, nil
}

// refreshAdminPost rewrites the admin post to show the current status.
func (s *Service) refreshAdminPost(ctx context.Context, r WFHRequest) {
	if r.AdminPostID == "" {
		return
	}
	used, err := s.usedThisMonth(ctx, r)
	if err != nil {
		s.Log.Warn("WFH used days", "err", err)
	}
	var atts []mattermost.Attachment
	if r.Status == WFHPending {
		atts = s.wfhButtons(r.ID, r.TeamID)
	}
	if err := s.Posts.UpdatePost(ctx, r.AdminPostID, s.adminPostText(r, used, false), atts); err != nil {
		s.Log.Warn("update WFH admin post", "wfh_id", r.ID, "err", err)
	}
}

func (s *Service) wfhList(ctx context.Context, c Command) (Reply, error) {
	rs, err := s.Store.ListWFH(ctx, c.UserID, c.TeamID, 15)
	if err != nil {
		return Reply{}, err
	}
	if len(rs) == 0 {
		return text("You have no WFH requests in **" + s.Teams.TeamName(c.TeamID) + "**. Use `/att wfh` to request one."), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#### Your WFH requests — %s\n| # | Dates | Status | Note |\n|---|---|---|---|\n", s.Teams.TeamName(c.TeamID))
	for _, r := range rs {
		note := ""
		switch r.Status {
		case WFHApproved:
			note = "by @" + r.DecidedByName
		case WFHRejected:
			note = "by @" + r.DecidedByName + ": " + r.Note
		}
		fmt.Fprintf(&b, "| %d | %s | %s %s | %s |\n", r.ID, s.wfhDates(r), statusIcon(r.Status), r.Status, note)
	}
	return text(strings.TrimRight(b.String(), "\n")), nil
}

func (s *Service) wfhCancel(ctx context.Context, c Command, args string) (Reply, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(args), "#"), 10, 64)
	if err != nil {
		return text("Usage: `/att wfh cancel <id>` (ids are in `/att wfh list`)."), nil
	}
	today := s.Now().In(s.Loc).Format(DayFormat)
	r, err := s.Store.CancelWFH(ctx, id, c.UserID, today)
	if err != nil {
		return Reply{}, err
	}
	if r == nil {
		return text(fmt.Sprintf("Request #%d can't be cancelled: it isn't yours, or it's already decided/started. See `/att wfh list`.", id)), nil
	}
	if err := s.Store.InsertAudit(ctx, AuditEntry{
		ActorID: c.UserID, TargetID: c.UserID, TeamID: r.TeamID, Action: "wfh_cancel",
		New: map[string]any{"wfh_id": id, "status": WFHCancelled},
	}); err != nil {
		s.Log.Error("WFH audit", "err", err)
	}
	s.refreshAdminPost(ctx, *r)
	return text(fmt.Sprintf("🚫 WFH request #%d (%s) cancelled.", id, s.wfhDates(*r))), nil
}

func (s *Service) wfhPending(ctx context.Context, c Command) (Reply, error) {
	if r, err := s.requireAdmin(ctx, c.TeamID, c.UserID); r != nil || err != nil {
		return deref(r), err
	}
	rs, err := s.Store.PendingWFH(ctx, c.TeamID, "")
	if err != nil {
		return Reply{}, err
	}
	return text(s.pendingTable("#### Pending WFH requests — "+s.Teams.TeamName(c.TeamID), rs)), nil
}

// wfhAllDays: how far back /att wfh all looks (by end date).
const wfhAllDays = 30

func (s *Service) wfhAll(ctx context.Context, c Command) (Reply, error) {
	if r, err := s.requireAdmin(ctx, c.TeamID, c.UserID); r != nil || err != nil {
		return deref(r), err
	}
	now := s.Now().In(s.Loc)
	from := now.AddDate(0, 0, -wfhAllDays).Format(DayFormat)
	rs, err := s.Store.ListTeamWFH(ctx, c.TeamID, from, 100)
	if err != nil {
		return Reply{}, err
	}
	return text(WFHAllTable(s.Teams.TeamName(c.TeamID), rs, now, s.Loc)), nil
}

// WFHAllTable renders a team's requests: who is WFH today first, then by date.
func WFHAllTable(team string, rs []WFHRequest, now time.Time, loc *time.Location) string {
	today := now.In(loc).Format(DayFormat)
	var b strings.Builder
	fmt.Fprintf(&b, "#### WFH requests — %s (last %d days and upcoming)\n", team, wfhAllDays)
	if len(rs) == 0 {
		b.WriteString("None.")
		return b.String()
	}
	var wfhToday []string
	for _, r := range rs {
		if r.Status == WFHApproved && r.Start <= today && today <= r.End {
			who := "@" + r.Username
			if r.HalfDay {
				who += " (½)"
			}
			wfhToday = append(wfhToday, who)
		}
	}
	if len(wfhToday) > 0 {
		fmt.Fprintf(&b, "🏠 **WFH today:** %s\n\n", strings.Join(wfhToday, ", "))
	} else {
		b.WriteString("🏠 Nobody is approved for WFH today.\n\n")
	}
	b.WriteString("| # | Member | Dates | Status | Decided by | Reason |\n|---|---|---|---|---|---|\n")
	for _, r := range rs {
		by := ""
		if r.DecidedByName != "" {
			by = "@" + r.DecidedByName
			if r.Status == WFHRejected && r.Note != "" {
				by += ": " + r.Note
			}
		}
		fmt.Fprintf(&b, "| %d | @%s | %s | %s %s | %s | %s |\n", r.ID, r.Username, wfhDatesIn(r, loc),
			statusIcon(r.Status), r.Status, by, strings.ReplaceAll(r.Reason, "\n", " "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *Service) pendingTable(title string, rs []WFHRequest) string {
	return pendingTable(title, rs, s.Loc)
}

func pendingTable(title string, rs []WFHRequest, loc *time.Location) string {
	if len(rs) == 0 {
		return title + "\nNone. 🎉"
	}
	var b strings.Builder
	b.WriteString(title + "\n| # | Member | Dates | Reason |\n|---|---|---|---|\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "| %d | @%s | %s | %s |\n", r.ID, r.Username, wfhDatesIn(r, loc), strings.ReplaceAll(r.Reason, "\n", " "))
	}
	b.WriteString("\nDecide with the buttons on each request in this channel.")
	return b.String()
}

// workMode returns "wfh" if the member has an approved full-day WFH request
// covering day in teamID, otherwise "office".
func (s *Service) workMode(ctx context.Context, userID, teamID string, day time.Time) (string, error) {
	ok, err := s.Store.WFHApprovedOn(ctx, userID, teamID, day.In(s.Loc).Format(DayFormat))
	if err != nil || !ok {
		return "office", err
	}
	return "wfh", nil
}

func ephemeral(msg string) Reply { return Reply{Text: msg, EphemeralOnly: true} }
