package attendance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/config"
	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// Errors returned by Store implementations.
var (
	ErrAlreadyCheckedIn = errors.New("already checked in")
	ErrNotCheckedIn     = errors.New("not checked in")
	ErrAlreadyOnBreak   = errors.New("already on break")
	ErrNotOnBreak       = errors.New("not on break")
)

// WorkingMember is one row of /att status.
type WorkingMember struct {
	UserID      string
	Username    string
	DisplayName string
	CheckIn     time.Time
	WorkMode    string
	BreakSince  *time.Time
}

// Store is the persistence the service needs (implemented by internal/store).
type Store interface {
	StatusStore
	AdminStore
	ReportStore
	WFHStore
	EODStore
	// MemberRefreshedAt reports when a member's profile was last synced.
	MemberRefreshedAt(ctx context.Context, userID string) (t time.Time, found bool, err error)
	UpsertMember(ctx context.Context, userID, username, displayName string) error
	// InsertSession returns ErrAlreadyCheckedIn if the user has any open session.
	InsertSession(ctx context.Context, userID, teamID string, checkIn time.Time, mode, note string) (int64, error)
	// OpenSession returns the user's open session with its breaks, or nil.
	OpenSession(ctx context.Context, userID string) (*Session, error)
	// CloseOpenSession closes the open session (and any open break) at out.
	CloseOpenSession(ctx context.Context, userID string, out time.Time, summary string) (*Session, error)
	// StartBreak / EndBreak return ErrNotCheckedIn, ErrAlreadyOnBreak, ErrNotOnBreak.
	StartBreak(ctx context.Context, userID string, at time.Time) (*Session, error)
	EndBreak(ctx context.Context, userID string, at time.Time) (*Session, error)
	// SessionsOverlapping returns the user's sessions (with breaks) that overlap [from, to).
	SessionsOverlapping(ctx context.Context, userID string, from, to time.Time) ([]Session, error)
	TeamOpenSessions(ctx context.Context, teamID string) ([]WorkingMember, error)
}

// Directory looks up Mattermost user profiles.
type Directory interface {
	UserProfile(ctx context.Context, userID string) (username, displayName string, err error)
}

// Teams resolves configured team ids to names.
type Teams interface {
	TeamName(teamID string) string
}

// Signer signs button contexts.
type Signer interface {
	Sign(fields map[string]string) map[string]string
	SignFor(fields map[string]string, ttl time.Duration) map[string]string
}

// Command is a verified slash command invocation.
type Command struct {
	TeamID    string
	UserID    string
	UserName  string
	ChannelID string
	TriggerID string // lets the service open a dialog
	Text      string // everything after "/att"
}

// Reply is an ephemeral reply, optionally with buttons.
type Reply struct {
	Text        string
	Attachments []mattermost.Attachment
	// EphemeralOnly (buttons): show Text privately to the clicker and leave
	// the clicked post unchanged.
	EphemeralOnly bool
}

func text(s string) Reply { return Reply{Text: s} }

// Service implements the /att commands and button clicks.
type Service struct {
	Store     Store
	Directory Directory
	Teams     Teams
	Status    *StatusUpdater // nil disables custom status updates
	Signer    Signer
	ActionURL string        // where Mattermost posts button clicks
	Admins    *AdminChecker // nil: nobody is admin
	People    PeopleAPI
	Roster    RosterAPI
	Files     FileSender
	Workdays  map[time.Weekday]bool
	Posts     Poster
	DialogURL string                 // where Mattermost posts dialog submissions
	TeamCfg   map[string]config.Team // channel ids per team
	Tasks     TaskSource             // nil: GitHub features off
	Loc       *time.Location
	Log       *slog.Logger
	Now       func() time.Time
}

const (
	maxNoteLen      = 500
	profileMaxAge   = 24 * time.Hour
	genericErrorMsg = "⚠️ Something went wrong. Please try again, or tell an admin if it keeps happening."
)

// Handle routes a slash command and returns the ephemeral reply.
func (s *Service) Handle(ctx context.Context, c Command) Reply {
	sub, rest := splitFirst(c.Text)
	log := s.Log.With("team_id", c.TeamID, "user_id", c.UserID, "sub", sub)

	if sub == "" || sub == "help" {
		return text(helpText(s.isAdmin(ctx, c.TeamID, c.UserID)))
	}
	if err := s.ensureMember(ctx, c.UserID, c.UserName); err != nil {
		log.Error("register member", "err", err)
		return text(genericErrorMsg)
	}

	var (
		r   Reply
		err error
	)
	switch sub {
	case "in":
		r, err = s.checkIn(ctx, c, rest)
	case "out":
		r, err = s.checkOut(ctx, c.UserID, rest, c.TriggerID)
	case "break":
		r, err = s.startBreak(ctx, c.UserID)
	case "back":
		r, err = s.endBreak(ctx, c.UserID)
	case "status":
		r, err = s.teamStatus(ctx, c.TeamID)
	case "hours":
		r, err = s.hours(ctx, c.UserID, rest)
	case "member":
		r, err = s.member(ctx, c, rest)
	case "holiday", "holidays":
		r, err = s.holiday(ctx, c, rest)
	case "report":
		r, err = s.report(ctx, c, rest)
	case "fix":
		r, err = s.fix(ctx, c, rest)
	case "export":
		r, err = s.export(ctx, c, rest)
	case "wfh":
		r, err = s.wfh(ctx, c, rest)
	case "github":
		r, err = s.github(ctx, c, rest)
	case "tasks", "task":
		r, err = s.tasks(ctx, c, rest)
	case "eod":
		r, err = s.eod(ctx, c, rest)
	default:
		return text(fmt.Sprintf("Unknown command `%s`.\n\n%s", sub, helpText(s.isAdmin(ctx, c.TeamID, c.UserID))))
	}
	if err != nil {
		log.Error("command failed", "err", err)
		return text(genericErrorMsg)
	}
	log.Info("command ok")
	return r
}

// Button actions carried in signed button contexts.
const (
	ActBreak = "break"
	ActBack  = "back"
	ActOut   = "out"
)

// HandleButton runs a verified button click.
func (s *Service) HandleButton(ctx context.Context, c Click) Reply {
	userID, action := c.UserID, c.Action
	log := s.Log.With("user_id", userID, "button", action)
	var (
		r   Reply
		err error
	)
	switch action {
	case ActWFHApprove, ActWFHReject:
		r, err = s.handleWFHClick(ctx, c)
	case ActBreak:
		r, err = s.startBreak(ctx, userID)
	case ActBack:
		r, err = s.endBreak(ctx, userID)
	case ActOut:
		r, err = s.checkOut(ctx, userID, "", c.TriggerID)
	case ActEOD:
		r, err = s.eodClick(ctx, c)
	default:
		return text("Unknown button.")
	}
	if err != nil {
		log.Error("button failed", "err", err)
		return text(genericErrorMsg)
	}
	log.Info("button ok")
	return r
}

// ensureMember registers the user on first use and refreshes their profile
// at most once a day. Keyed on user id, never username.
func (s *Service) ensureMember(ctx context.Context, userID, fallbackName string) error {
	refreshed, found, err := s.Store.MemberRefreshedAt(ctx, userID)
	if err != nil {
		return err
	}
	if found && s.Now().Sub(refreshed) < profileMaxAge {
		return nil
	}
	username, display, err := s.Directory.UserProfile(ctx, userID)
	if err != nil {
		s.Log.Warn("profile fetch failed; using slash payload", "user_id", userID, "err", err)
		if found {
			return nil // keep the stored profile, retry next time
		}
		username, display = fallbackName, ""
	}
	return s.Store.UpsertMember(ctx, userID, username, display)
}

func (s *Service) checkIn(ctx context.Context, c Command, note string) (Reply, error) {
	now := s.Now()
	note = truncate(note, maxNoteLen)
	// Members can't choose the mode: an approved full-day WFH request does.
	mode, err := s.workMode(ctx, c.UserID, c.TeamID, now)
	if err != nil {
		return Reply{}, err
	}
	_, err = s.Store.InsertSession(ctx, c.UserID, c.TeamID, now, mode, note)
	if errors.Is(err, ErrAlreadyCheckedIn) {
		open, oerr := s.Store.OpenSession(ctx, c.UserID)
		if oerr != nil || open == nil {
			return text("You're already checked in."), oerr
		}
		r := s.withButtons(fmt.Sprintf("You're already checked in to **%s** since %s. Use `/att out` first.",
			s.Teams.TeamName(open.TeamID), s.fmtSince(open.CheckIn, now)), c.UserID, onBreak(open))
		return r, nil
	}
	if err != nil {
		return Reply{}, err
	}
	s.Status.Enqueue(c.UserID, workingKind(mode), true)

	msg := fmt.Sprintf("✅ Checked in to **%s** at %s · %s", s.Teams.TeamName(c.TeamID), s.fmtClock(now), modeLabel(mode))
	if note != "" {
		msg += "\n> " + note
	}
	return s.withButtons(msg, c.UserID, false), nil
}

// checkOut closes the open session and opens the end-of-day form for the
// session's team (when triggerID allows); the reply carries a button to
// reopen the form.
func (s *Service) checkOut(ctx context.Context, userID, summary, triggerID string) (Reply, error) {
	now := s.Now()
	sess, err := s.Store.CloseOpenSession(ctx, userID, now, truncate(summary, maxNoteLen))
	if errors.Is(err, ErrNotCheckedIn) {
		return text("You're not checked in. Use `/att in` to start."), nil
	}
	if err != nil {
		return Reply{}, err
	}
	s.Status.Enqueue(userID, StatusOff, false)

	var sessionTotal time.Duration
	for _, v := range NetByDay([]Session{*sess}, now, s.Loc) {
		sessionTotal += v
	}
	perTeam, err := s.teamTotals(ctx, userID, now, now)
	if err != nil {
		return Reply{}, err
	}
	msg := fmt.Sprintf("🔴 Checked out of **%s** at %s · this session %s\n%s",
		s.Teams.TeamName(sess.TeamID), s.fmtClock(now), FormatDuration(sessionTotal),
		s.todayLine(perTeam))

	day := sess.CheckIn.In(s.Loc).Format(DayFormat) // the report is for the day the session started
	if triggerID != "" {
		if err := s.openEODForm(ctx, userID, sess.TeamID, day, triggerID); err != nil {
			s.Log.Warn("eod: open form on check-out failed", "user_id", userID, "err", err)
		}
	}
	return Reply{
		Text:        msg + "\n📝 Fill in your end-of-day report (reopen it with the button or `/att eod submit`).",
		Attachments: s.eodButton(userID, sess.TeamID, day),
	}, nil
}

func (s *Service) startBreak(ctx context.Context, userID string) (Reply, error) {
	now := s.Now()
	sess, err := s.Store.StartBreak(ctx, userID, now)
	switch {
	case errors.Is(err, ErrNotCheckedIn):
		return text("You're not checked in, so you can't take a break. Use `/att in` first."), nil
	case errors.Is(err, ErrAlreadyOnBreak):
		return s.withButtons("You're already on a break. Use `/att back` when you return.", userID, true), nil
	case err != nil:
		return Reply{}, err
	}
	s.Status.Enqueue(userID, StatusOnBreak, false)
	return s.withButtons(fmt.Sprintf("☕ On break since %s (%s). Use `/att back` when you return.",
		s.fmtClock(now), s.Teams.TeamName(sess.TeamID)), userID, true), nil
}

func (s *Service) endBreak(ctx context.Context, userID string) (Reply, error) {
	now := s.Now()
	sess, err := s.Store.EndBreak(ctx, userID, now)
	switch {
	case errors.Is(err, ErrNotCheckedIn):
		return text("You're not checked in. Use `/att in` to start."), nil
	case errors.Is(err, ErrNotOnBreak):
		return s.withButtons("You're not on a break.", userID, false), nil
	case err != nil:
		return Reply{}, err
	}
	s.Status.Enqueue(userID, workingKind(sess.WorkMode), false)

	msg := fmt.Sprintf("▶️ Back at %s (%s).", s.fmtClock(now), s.Teams.TeamName(sess.TeamID))
	if open, err := s.Store.OpenSession(ctx, userID); err == nil && open != nil && len(open.Breaks) > 0 {
		last := open.Breaks[len(open.Breaks)-1]
		if last.End != nil {
			msg = fmt.Sprintf("▶️ Back at %s (%s) · break %s.", s.fmtClock(now),
				s.Teams.TeamName(sess.TeamID), FormatDuration(last.End.Sub(last.Start)))
		}
	}
	return s.withButtons(msg, userID, false), nil
}

func (s *Service) teamStatus(ctx context.Context, teamID string) (Reply, error) {
	now := s.Now()
	rows, err := s.Store.TeamOpenSessions(ctx, teamID)
	if err != nil {
		return Reply{}, err
	}
	name := s.Teams.TeamName(teamID)
	if len(rows) == 0 {
		return text(fmt.Sprintf("Nobody is checked in to **%s** right now.", name)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#### Working now in %s (%d)\n| Member | Since | Mode | Status |\n|---|---|---|---|\n", name, len(rows))
	for _, w := range rows {
		state := "🟢 Working"
		if w.BreakSince != nil {
			state = "☕ On break since " + s.fmtClock(*w.BreakSince)
		}
		fmt.Fprintf(&b, "| @%s | %s | %s | %s |\n", w.Username, s.fmtSince(w.CheckIn, now), modeIcon(w.WorkMode), state)
	}
	return text(strings.TrimRight(b.String(), "\n")), nil
}

// withButtons attaches the session buttons for the current state.
func (s *Service) withButtons(msg, userID string, onBreak bool) Reply {
	return Reply{Text: msg, Attachments: s.sessionButtons(userID, onBreak)}
}

func (s *Service) sessionButtons(userID string, onBreak bool) []mattermost.Attachment {
	if s.Signer == nil || s.ActionURL == "" {
		return nil
	}
	button := func(id, label, style, action string) mattermost.Action {
		return mattermost.Action{
			ID: id, Name: label, Style: style,
			Integration: mattermost.Integration{
				URL:     s.ActionURL,
				Context: s.Signer.Sign(map[string]string{"a": action, "u": userID}),
			},
		}
	}
	first := button("attbreak", "☕ Break", "default", ActBreak)
	if onBreak {
		first = button("attback", "▶️ Back", "primary", ActBack)
	}
	return []mattermost.Attachment{{Actions: []mattermost.Action{first, button("attout", "🔴 Check out", "danger", ActOut)}}}
}

// TeamConfig returns the configured team (channels etc.).
func (s *Service) TeamConfig(teamID string) (config.Team, bool) {
	t, ok := s.TeamCfg[teamID]
	return t, ok
}

func onBreak(s *Session) bool {
	return s != nil && len(s.Breaks) > 0 && s.Breaks[len(s.Breaks)-1].End == nil
}

func workingKind(mode string) StatusKind {
	if mode == "wfh" {
		return StatusWorkingFromHome
	}
	return StatusWorking
}

func modeIcon(mode string) string {
	if mode == "wfh" {
		return "🏠"
	}
	return "🏢"
}

func modeLabel(mode string) string {
	if mode == "wfh" {
		return "🏠 Working from home"
	}
	return "🏢 Office"
}

func (s *Service) fmtClock(t time.Time) string { return t.In(s.Loc).Format("3:04 PM") }

// fmtSince shows only the time for today, otherwise date and time.
func (s *Service) fmtSince(t, now time.Time) string {
	if t.In(s.Loc).Format(DayFormat) == now.In(s.Loc).Format(DayFormat) {
		return s.fmtClock(t)
	}
	return t.In(s.Loc).Format("2 Jan, 3:04 PM")
}

func splitFirst(s string) (string, string) {
	s = strings.TrimSpace(s)
	first, rest, _ := strings.Cut(s, " ")
	return strings.ToLower(first), strings.TrimSpace(rest)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// isAdmin is the admin check for display purposes only (errors → false).
func (s *Service) isAdmin(ctx context.Context, teamID, userID string) bool {
	if s.Admins == nil {
		return false
	}
	ok, err := s.Admins.IsAdmin(ctx, teamID, userID)
	if err != nil {
		s.Log.Warn("admin check failed", "user_id", userID, "err", err)
	}
	return ok
}

func helpText(admin bool) string {
	lines := []string{
		"#### Attendance commands",
		"| Command | What it does |",
		"|---|---|",
		"| `/att in [note]` | Check in to this team |",
		"| `/att out [summary]` | Check out and see today's total |",
		"| `/att break` / `/att back` | Start / end a break (not counted as work) |",
		"| `/att status` | Who is working in this team right now |",
		"| `/att hours [today\\|week\\|month]` | Your hours, per day and per organization |",
		"| `/att holiday list` | Upcoming holidays |",
		"| `/att wfh` | Request work from home (opens a form) |",
		"| `/att wfh list` / `/att wfh cancel <id>` | Your WFH requests / cancel one |",
		"| `/att github` | Show your linked GitHub login (admins link accounts) |",
		"| `/att tasks` | Your open tasks on this team's GitHub Project |",
		"| `/att eod` / `/att eod submit` | Your end-of-day report for today / open the report form |",
		"| `/att help` | This list |",
	}
	if admin {
		lines = append(lines,
			"",
			"#### Admin commands (this team)",
			"| Command | What it does |",
			"|---|---|",
			"| `/att member @user` | Show a member's settings |",
			"| `/att member @user tracked on\\|off` | Include in reminders and summaries |",
			"| `/att member @user reminders on\\|off` | 10:30 reminder DM |",
			"| `/att member @user hours <n>` | Expected hours per day |",
			"| `/att member @user github <username>\\|none` | Link / unlink a member's GitHub login |",
			"| `/att holiday add YYYY-MM-DD [all] <name>` | Add a holiday (`all` = every org, system admins) |",
			"| `/att holiday remove <id>` | Remove a holiday |",
			"| `/att report @user [week\\|month]` | A member's hours in this team |",
			"| `/att report team [week\\|month]` | Every member of this team |",
			"| `/att fix @user YYYY-MM-DD in HH:MM out HH:MM <reason>` | Replace a member's day in this team (audited) |",
			"| `/att export [YYYY-MM]` | CSV of this team's sessions, sent to you by DM |",
			"| `/att wfh pending` | Pending WFH requests (decide with the buttons in #attendance-admin) |",
			"| `/att wfh all` | Every WFH request in this team: who's WFH today, last 30 days and upcoming |",
		)
	}
	return strings.Join(lines, "\n")
}
