package attendance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// End-of-day reports (tasks spec §7). A report belongs to one member, one
// team and one local day. Phase 2 saves it and posts it in the team's
// daily EOD thread; status changes are synced to GitHub in phase 3.

const (
	ActEOD = "eod" // personal button: open the EOD form
	dlgEOD = "eod"

	// maxEODSelects is how many task dropdowns the form shows (spec §7.2).
	maxEODSelects = 8
	// maxEODOthers caps the "Other tasks I finished" choices.
	maxEODOthers = 25
	minDidLen    = 10
	maxEODText   = 2000
	// eodLateUntil: yesterday's report can still be submitted until 11:00.
	eodLateUntil = 11
	// githubDialogBudget keeps the form within Mattermost's trigger-id
	// lifetime (3 s): past it the form opens without the task list.
	githubDialogBudget = 1500 * time.Millisecond

	noChange = "no_change"
)

// eodStatuses are the choices for each task in the form.
var eodStatuses = []string{StatusInProgress, StatusInReview, StatusBlocked, StatusDone}

// EODItem is one issue in a report.
type EODItem struct {
	IssueURL  string
	Title     string
	OldStatus string
	NewStatus string // = OldStatus when unchanged
	Synced    bool   // nothing (left) to write to GitHub
}

// Changed reports whether the member changed the status.
func (i EODItem) Changed() bool { return i.NewStatus != i.OldStatus }

// EODReport is one member's report for one team and day.
type EODReport struct {
	ID          int64
	UserID      string
	TeamID      string
	Day         string // YYYY-MM-DD
	DidToday    string
	Blockers    string
	Tomorrow    string
	Items       []EODItem
	SyncStatus  string
	ReplyPostID string
	SubmittedAt time.Time
	UpdatedAt   time.Time

	// Phase 3 (GitHub sync).
	SyncError     string
	SyncAttempts  int
	SyncVersion   int // bumped by every save; a sync only finishes its own version
	Activity      *Activity
	BlockerPostID string
}

// EODStore persists reports and the daily threads.
type EODStore interface {
	// SaveEODReport inserts or updates the report for (user, team, day),
	// replacing its items. It returns the stored report (ID, ReplyPostID).
	SaveEODReport(ctx context.Context, r EODReport) (EODReport, error)
	GetEODReport(ctx context.Context, userID, teamID, day string) (*EODReport, error)
	// EnsureEODThread returns the team's root post for day, calling create
	// exactly once across concurrent callers when there is none.
	EnsureEODThread(ctx context.Context, teamID, day string, create func() (string, error)) (string, error)
	// EnsureEODReply returns the report's reply post id, calling create
	// (once) when it has none; created says whether create ran.
	EnsureEODReply(ctx context.Context, reportID int64, create func() (string, error)) (postID string, created bool, err error)
}

// --- picking the form's tasks ---

// RelevantIssues splits a member's tasks into the form's dropdowns and the
// "already done today" list. Dropdowns: tasks picked today or currently In
// progress / In review / Blocked, de-duplicated, without the closed ones,
// at most maxEODSelects (the most recently updated); omitted counts the rest.
func RelevantIssues(open, closedToday []Task, picked map[string]bool) (selects, done []Task, omitted int) {
	isDone := map[string]bool{}
	for _, t := range closedToday {
		if !isDone[t.URL] {
			isDone[t.URL] = true
			done = append(done, t)
		}
	}
	seen := map[string]bool{}
	for _, t := range open {
		if seen[t.URL] || isDone[t.URL] {
			continue
		}
		active := t.Status == StatusInProgress || t.Status == StatusInReview || t.Status == StatusBlocked
		if !active && !picked[t.URL] {
			continue
		}
		seen[t.URL] = true
		selects = append(selects, t)
	}
	sort.SliceStable(selects, func(i, j int) bool { return selects[i].Updated.After(selects[j].Updated) })
	if len(selects) > maxEODSelects {
		omitted = len(selects) - maxEODSelects
		selects = selects[:maxEODSelects]
	}
	sort.SliceStable(done, func(i, j int) bool { return done[i].Number < done[j].Number })
	return selects, done, omitted
}

// formIssue is an issue carried in the signed form state.
type formIssue struct {
	URL    string `json:"u"`
	Title  string `json:"t"`
	Status string `json:"s,omitempty"`
}

func toFormIssues(ts []Task) []formIssue {
	out := make([]formIssue, 0, len(ts))
	for _, t := range ts {
		out = append(out, formIssue{URL: t.URL, Title: truncate(t.Title, 100), Status: t.Status})
	}
	return out
}

// --- opening the form ---

// eodDay picks the day `/att eod submit` reports on: today if the member
// worked in the team today, else yesterday (until 11:00) if they worked
// then. ok is false when there's nothing to report on.
func (s *Service) eodDay(ctx context.Context, userID, teamID string) (string, bool, error) {
	now := s.Now().In(s.Loc)
	today, _ := DayBounds(now, s.Loc)
	days := []time.Time{today}
	if now.Hour() < eodLateUntil {
		days = append(days, today.AddDate(0, 0, -1))
	}
	for _, d := range days {
		_, _, worked, err := s.dayWork(ctx, userID, teamID, d.Format(DayFormat))
		if err != nil {
			return "", false, err
		}
		if worked {
			return d.Format(DayFormat), true, nil
		}
	}
	return "", false, nil
}

// dayWork is the member's net time and work modes in teamID on day.
func (s *Service) dayWork(ctx context.Context, userID, teamID, day string) (time.Duration, map[string]bool, bool, error) {
	d, err := time.ParseInLocation(DayFormat, day, s.Loc)
	if err != nil {
		return 0, nil, false, err
	}
	from, to := DayBounds(d, s.Loc)
	sessions, err := s.Store.SessionsOverlapping(ctx, userID, from, to)
	if err != nil {
		return 0, nil, false, err
	}
	modes, worked := map[string]bool{}, false
	var net time.Duration
	now := s.Now()
	for _, sess := range sessions {
		if sess.TeamID != teamID {
			continue
		}
		worked = true
		modes[sess.WorkMode] = true
		net += NetByDay([]Session{sess}, now, s.Loc)[DayKey{Day: day, TeamID: teamID}]
	}
	return net, modes, worked, nil
}

// openEODForm opens the EOD form for (team, day). GitHub problems only
// remove the task dropdowns; they never block the form.
func (s *Service) openEODForm(ctx context.Context, userID, teamID, day, triggerID string) error {
	if triggerID == "" || s.Posts == nil {
		return errors.New("no trigger id")
	}
	f, err := s.eodFormData(ctx, userID, teamID, day, githubDialogBudget)
	if err != nil {
		return err
	}
	d := s.eodDialog(userID, teamID, day, f.selects, f.done, f.others, f.omitted, f.note, f.prev)
	return s.Posts.OpenDialog(ctx, triggerID, s.DialogURL, d)
}

// eodForm is what the end-of-day form shows: tasks with a status row
// (selects), tasks closed that day (done), the member's other open tasks
// (others), a note about missing tasks, and the report already saved for
// the day (prev). Shared by the dialog and the panel.
type eodForm struct {
	selects, done, others []Task
	omitted               int
	note                  string
	prev                  *EODReport
}

// eodFormData gathers the form's content; GitHub gets githubBudget (the
// dialog must open within Mattermost's ~3 s trigger window).
func (s *Service) eodFormData(ctx context.Context, userID, teamID, day string, githubBudget time.Duration) (eodForm, error) {
	var (
		selects, done, others []Task
		omitted               int
		note                  string
	)
	ms, err := s.Store.GetMember(ctx, userID)
	if err != nil {
		return eodForm{}, err
	}
	switch {
	case ms == nil || ms.GitHubLogin == "":
		if s.Tasks != nil && s.Tasks.Configured(teamID) {
			note = "_Your GitHub account isn't linked, so tasks aren't listed. Ask an admin to link it._"
		}
	case s.Tasks != nil && s.Tasks.Configured(teamID):
		gctx, cancel := context.WithTimeout(ctx, githubBudget)
		open, closed, err := s.Tasks.EODTasks(gctx, teamID, ms.GitHubLogin, s.dayStart(day))
		cancel()
		if err != nil {
			s.Log.Warn("eod: github tasks failed; form without tasks", "team_id", teamID, "user_id", userID, "err", err)
			note = "_Couldn't load your GitHub tasks right now; you can still submit the report._"
		} else {
			s.addToBoardLater(teamID, open)
			selects, done, omitted = RelevantIssues(open, closed, s.pickedIssues(ctx, userID, teamID, day))
			others = otherTasks(open, selects, done, s.Now().In(s.Loc).Format(DayFormat))
		}
	}

	prev, err := s.Store.GetEODReport(ctx, userID, teamID, day)
	if err != nil {
		return eodForm{}, err
	}
	return eodForm{selects: selects, done: done, others: others, omitted: omitted, note: note, prev: prev}, nil
}

// otherTasks are the member's open tasks without a dropdown, offered in
// "Other tasks I finished" (so a finished Todo task can be reported).
func otherTasks(open, selects, done []Task, today string) []Task {
	skip := map[string]bool{}
	for _, t := range append(append([]Task(nil), selects...), done...) {
		skip[t.URL] = true
	}
	var out []Task
	for _, t := range open {
		if !skip[t.URL] {
			skip[t.URL] = true
			out = append(out, t)
		}
	}
	SortTasks(out, today)
	if len(out) > maxEODOthers {
		out = out[:maxEODOthers]
	}
	return out
}

// dayStart is the start of day (YYYY-MM-DD) in the configured timezone.
func (s *Service) dayStart(day string) time.Time {
	d, _ := time.ParseInLocation(DayFormat, day, s.Loc)
	return d
}

// eodDialog builds the form. prev (a report already submitted for the
// day) pre-fills it, so a resubmission edits rather than starts over.
func (s *Service) eodDialog(userID, teamID, day string, selects, done, others []Task, omitted int, note string, prev *EODReport) mattermost.Dialog {
	var intro []string
	when := s.dayStart(day).Format("Mon 2 Jan")
	intro = append(intro, fmt.Sprintf("**%s** · %s", s.Teams.TeamName(teamID), when))
	if len(done) > 0 {
		var refs []string
		for _, t := range done {
			refs = append(refs, fmt.Sprintf("#%d", t.Number))
		}
		intro = append(intro, "✅ Already done today: "+strings.Join(refs, ", "))
	}
	if omitted > 0 {
		intro = append(intro, fmt.Sprintf("_%d more active tasks aren't shown (the form lists the %d most recently updated)._", omitted, maxEODSelects))
	}
	if note != "" {
		intro = append(intro, note)
	}
	if prev != nil {
		intro = append(intro, "_You already submitted a report for this day; submitting again updates it._")
	}

	prevStatus := map[string]string{}
	var did, blockers, tomorrow string
	if prev != nil {
		for _, it := range prev.Items {
			prevStatus[it.IssueURL] = it.NewStatus
		}
		did, blockers, tomorrow = prev.DidToday, prev.Blockers, prev.Tomorrow
	}

	options := []mattermost.DialogOption{{Text: "No change", Value: noChange}}
	for _, st := range eodStatuses {
		options = append(options, mattermost.DialogOption{Text: st, Value: st})
	}
	var elements []mattermost.DialogElement
	for i, t := range selects {
		def := noChange
		if st, ok := prevStatus[t.URL]; ok && st != t.Status {
			def = st
		}
		cur := t.Status
		if cur == "" {
			cur = "No status"
		}
		elements = append(elements, mattermost.DialogElement{
			DisplayName: clip(fmt.Sprintf("#%d %s", t.Number, t.Title), 24),
			Name:        "s" + strconv.Itoa(i),
			Type:        "select",
			Default:     def,
			HelpText:    clip(fmt.Sprintf("%s (now: %s)", t.Title, cur), 150),
			Options:     options,
		})
	}
	if len(others) > 0 {
		var opts []mattermost.DialogOption
		var defs []string
		for i, t := range others {
			opts = append(opts, mattermost.DialogOption{Text: clip(fmt.Sprintf("#%d %s", t.Number, t.Title), 80), Value: strconv.Itoa(i)})
			if prevStatus[t.URL] == StatusDone {
				defs = append(defs, strconv.Itoa(i))
			}
		}
		elements = append(elements, mattermost.DialogElement{
			DisplayName: "Other tasks I finished", Name: "others", Type: "select", MultiSelect: true, Optional: true,
			Default: strings.Join(defs, ","), Options: opts,
			HelpText: "Finished something that isn't listed above? Pick it here; it's recorded as Done.",
		})
	}
	elements = append(elements,
		mattermost.DialogElement{DisplayName: "What I did today", Name: "did", Type: "textarea",
			MinLength: minDidLen, MaxLength: maxEODText, Default: did,
			Placeholder: "e.g. Finished session timeout handling, reviewed the CSV export"},
		mattermost.DialogElement{DisplayName: "Blockers", Name: "blockers", Type: "textarea", Optional: true,
			MaxLength: maxEODText, Default: blockers, Placeholder: "Anything stopping you?"},
		mattermost.DialogElement{DisplayName: "Plan for tomorrow", Name: "tomorrow", Type: "textarea", Optional: true,
			MaxLength: maxEODText, Default: tomorrow},
	)

	return mattermost.Dialog{
		CallbackID:       dlgEOD,
		Title:            "End of day report",
		IntroductionText: strings.Join(intro, "\n"),
		SubmitLabel:      "Submit",
		State:            s.eodState(userID, teamID, day, eodForm{selects: selects, done: done, others: others}),
		Elements:         elements,
	}
}

// eodState is the signed form state (shared by the dialog and the panel):
// who, which team and day, and the task lists the submission refers to.
func (s *Service) eodState(userID, teamID, day string, f eodForm) string {
	items, _ := json.Marshal(toFormIssues(f.selects))
	doneJSON, _ := json.Marshal(toFormIssues(f.done))
	othersJSON, _ := json.Marshal(toFormIssues(f.others))
	return s.signedState(map[string]string{
		"k": dlgEOD, "u": userID, "t": teamID, "d": day,
		"items": string(items), "done": string(doneJSON), "others": string(othersJSON),
	})
}

// multiValues reads a multi-select submission: a list of strings (what
// Mattermost sends) or a comma-separated string.
func multiValues(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case []string:
		for _, s := range x {
			if strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		for _, s := range strings.Split(x, ",") {
			if strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

// clip shortens s to n runes, ending with "…" when cut.
func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// --- submitting ---

func (s *Service) submitEOD(ctx context.Context, d DialogSubmit) (DialogResult, error) {
	teamID, day := d.Fields["t"], d.Fields["d"]
	if teamID == "" || !dayRe.MatchString(day) {
		return DialogResult{Error: "This form is invalid. Please run `/att eod submit`."}, nil
	}
	if !s.eodDayOpen(day) {
		return DialogResult{Error: s.eodClosedMsg(day)}, nil
	}
	str := func(k string) string { v, _ := d.Submission[k].(string); return strings.TrimSpace(v) }
	did := str("did")
	if len([]rune(did)) < minDidLen {
		return DialogResult{Errors: map[string]string{"did": fmt.Sprintf("Write at least %d characters.", minDidLen)}}, nil
	}
	var selects, done, others []formIssue
	if json.Unmarshal([]byte(d.Fields["items"]), &selects) != nil || json.Unmarshal([]byte(d.Fields["done"]), &done) != nil {
		return DialogResult{Error: "This form is invalid. Please run `/att eod submit`."}, nil
	}
	if o := d.Fields["others"]; o != "" && json.Unmarshal([]byte(o), &others) != nil {
		return DialogResult{Error: "This form is invalid. Please run `/att eod submit`."}, nil
	}

	allowed := map[string]bool{noChange: true}
	for _, st := range eodStatuses {
		allowed[st] = true
	}
	var items []EODItem
	for i, fi := range selects {
		choice := str("s" + strconv.Itoa(i))
		if choice == "" {
			choice = noChange
		}
		if !allowed[choice] {
			return DialogResult{Errors: map[string]string{"s" + strconv.Itoa(i): "Pick one of the listed options."}}, nil
		}
		it := EODItem{IssueURL: fi.URL, Title: fi.Title, OldStatus: fi.Status, NewStatus: fi.Status}
		if choice != noChange {
			it.NewStatus = choice
		}
		it.Synced = !it.Changed()
		items = append(items, it)
	}
	seenOther := map[int]bool{}
	for _, v := range multiValues(d.Submission["others"]) {
		i, err := strconv.Atoi(v)
		if err != nil || i < 0 || i >= len(others) {
			return DialogResult{Errors: map[string]string{"others": "Pick tasks from the list."}}, nil
		}
		if seenOther[i] {
			continue
		}
		seenOther[i] = true
		fi := others[i]
		items = append(items, EODItem{IssueURL: fi.URL, Title: fi.Title, OldStatus: fi.Status, NewStatus: StatusDone, Synced: fi.Status == StatusDone})
	}
	for _, fi := range done { // closed today: already Done on GitHub
		items = append(items, EODItem{IssueURL: fi.URL, Title: fi.Title, OldStatus: fi.Status, NewStatus: StatusDone, Synced: true})
	}

	ms, err := s.Store.GetMember(ctx, d.UserID)
	if err != nil {
		return DialogResult{}, err
	}
	// GitHub work to do (status changes, activity) only with a linked login
	// on a team with GitHub; otherwise the report is final as saved.
	sync := "synced"
	if ms != nil && ms.GitHubLogin != "" && s.Tasks != nil && s.Tasks.Configured(teamID) && s.Writer != nil {
		sync = "pending"
	}
	r, err := s.Store.SaveEODReport(ctx, EODReport{
		UserID: d.UserID, TeamID: teamID, Day: day, DidToday: truncate(did, maxEODText),
		Blockers: truncate(str("blockers"), maxEODText), Tomorrow: truncate(str("tomorrow"), maxEODText),
		Items: items, SyncStatus: sync,
	})
	if err != nil {
		return DialogResult{}, err
	}

	confirm := "📝 End-of-day report saved and posted in **~attendance**."
	if err := s.postEODReply(ctx, r); err != nil {
		s.Log.Error("eod: posting to thread failed", "team_id", teamID, "user_id", d.UserID, "err", err)
		confirm = "📝 End-of-day report saved, but it couldn't be posted in ~attendance right now. It's kept; `/att eod` shows it."
	}
	if fresh, err := s.Store.GetEODReport(ctx, d.UserID, teamID, day); err == nil && fresh != nil {
		username := d.UserID
		if ms != nil {
			username = ms.Username
		}
		if err := s.alertBlockers(ctx, *fresh, username); err != nil {
			s.Log.Error("eod: blocker alert failed", "team_id", teamID, "user_id", d.UserID, "err", err)
		}
	}
	if sync == "pending" {
		confirm += " Task updates are being written to GitHub."
		s.enqueueEODSync(r.ID)
	}
	s.notify(d.UserID, EventEOD, teamID)
	if s.Posts != nil && d.ChannelID != "" {
		if err := s.Posts.EphemeralPost(ctx, d.UserID, d.ChannelID, confirm); err != nil {
			s.Log.Warn("eod: confirmation failed", "user_id", d.UserID, "err", err)
		}
	}
	return DialogResult{}, nil
}

// postEODReply posts the report in the team's EOD thread for its day, or
// edits the reply posted by an earlier submission.
func (s *Service) postEODReply(ctx context.Context, r EODReport) error {
	channel := s.TeamCfg[r.TeamID].AttendanceChannelID
	if channel == "" || s.Posts == nil {
		return fmt.Errorf("no #attendance channel configured for team %s", r.TeamID)
	}
	msg, err := s.renderEODForPost(ctx, r)
	if err != nil {
		return err
	}
	root, err := s.Store.EnsureEODThread(ctx, r.TeamID, r.Day, func() (string, error) {
		return s.Posts.CreatePostWithButtons(ctx, channel, "📝 **EOD reports — "+s.dayStart(r.Day).Format("Mon 2 Jan")+"**", nil)
	})
	if err != nil {
		return err
	}
	postID, created, err := s.Store.EnsureEODReply(ctx, r.ID, func() (string, error) {
		return s.Posts.CreateReply(ctx, channel, root, msg)
	})
	if err != nil || created {
		return err
	}
	return s.Posts.UpdatePost(ctx, postID, msg, nil)
}

func (s *Service) renderEODForPost(ctx context.Context, r EODReport) (string, error) {
	net, modes, _, err := s.dayWork(ctx, r.UserID, r.TeamID, r.Day)
	if err != nil {
		return "", err
	}
	name := ""
	if ms, err := s.Store.GetMember(ctx, r.UserID); err == nil && ms != nil {
		name = ms.DisplayName
		if name == "" {
			name = ms.Username
		}
	}
	return RenderEOD(name, net, modes, r), nil
}

// --- rendering ---

var (
	issueURLRe  = regexp.MustCompile(`/([^/]+)/issues/(\d+)$`)
	massMention = regexp.MustCompile(`(?i)@(channel|all|here)\b`)
)

// issueRef is "#42" for an issue URL.
func issueRef(url string) string {
	if m := issueURLRe.FindStringSubmatch(url); m != nil {
		return "#" + m[2]
	}
	return "issue"
}

// issueNum is the issue number in url (0 if none), for ordering.
func issueNum(url string) int {
	if m := issueURLRe.FindStringSubmatch(url); m != nil {
		n, _ := strconv.Atoi(m[2])
		return n
	}
	return 0
}

// safeText keeps member text from notifying a whole channel or breaking
// the post layout.
func safeText(s string) string {
	return massMention.ReplaceAllString(strings.TrimSpace(s), "@​$1")
}

var statusLines = []struct{ status, label string }{
	{StatusDone, "✅ Done"},
	{StatusInReview, "👀 In review"},
	{StatusInProgress, "🔄 In progress"},
	{StatusBlocked, "🚧 Blocked"},
	{StatusTodo, "📋 Todo"},
}

// RenderEOD is the report as posted in the thread (spec §7 format).
// Empty lines are left out.
func RenderEOD(name string, net time.Duration, modes map[string]bool, r EODReport) string {
	var b strings.Builder
	head := "📝 **" + safeText(name) + "** — " + FormatDuration(net)
	if icons := modesIcons(modes); icons != "" {
		mode := "office"
		if modes["wfh"] && !modes["office"] {
			mode = "WFH"
		} else if modes["wfh"] {
			mode = "office + WFH"
		}
		head += " (" + icons + " " + mode + ")"
	}
	b.WriteString(head + "\n")

	items := append([]EODItem(nil), r.Items...)
	sort.SliceStable(items, func(i, j int) bool { return issueNum(items[i].IssueURL) < issueNum(items[j].IssueURL) })
	byStatus := map[string][]string{}
	for _, it := range items {
		byStatus[it.NewStatus] = append(byStatus[it.NewStatus],
			fmt.Sprintf("[%s](%s) %s", issueRef(it.IssueURL), it.IssueURL, mdEscape(it.Title)))
	}
	for _, sl := range statusLines {
		if refs := byStatus[sl.status]; len(refs) > 0 {
			b.WriteString(sl.label + ": " + strings.Join(refs, ", ") + "\n")
		}
	}
	for _, f := range []struct{ label, text string }{
		{"Did", r.DidToday}, {"Blockers", r.Blockers}, {"Tomorrow", r.Tomorrow},
	} {
		if t := safeText(f.text); t != "" {
			b.WriteString("**" + f.label + ":** " + t + "\n")
		}
	}
	if gh := RenderActivity(r.Activity); gh != "" {
		b.WriteString(gh + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- /att eod ---

const eodUsage = "Usage:\n" +
	"- `/att eod` — your end-of-day report for today in this team\n" +
	"- `/att eod submit` — open the report form (today, or yesterday until 11:00)\n" +
	"- " + eodAdminUsage

func (s *Service) eod(ctx context.Context, c Command, args string) (Reply, error) {
	if f := strings.Fields(args); len(f) > 0 && (strings.HasPrefix(f[0], "@") || strings.EqualFold(f[0], "team")) {
		return s.eodAdmin(ctx, c, f)
	}
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "":
		return s.eodShow(ctx, c)
	case "submit":
		day, ok, err := s.eodDay(ctx, c.UserID, c.TeamID)
		if err != nil {
			return Reply{}, err
		}
		if !ok {
			return text("You haven't worked in **" + s.Teams.TeamName(c.TeamID) + "** today, so there's nothing to report. (Yesterday's report can be sent until 11:00.)"), nil
		}
		if err := s.openEODForm(ctx, c.UserID, c.TeamID, day, c.TriggerID); err != nil {
			s.Log.Warn("eod: open form failed", "user_id", c.UserID, "err", err)
			return text("Couldn't open the form. Please run `/att eod submit` again."), nil
		}
		return Reply{}, nil // the form is the response
	}
	return text(eodUsage), nil
}

func (s *Service) eodShow(ctx context.Context, c Command) (Reply, error) {
	today := s.Now().In(s.Loc).Format(DayFormat)
	r, err := s.Store.GetEODReport(ctx, c.UserID, c.TeamID, today)
	if err != nil {
		return Reply{}, err
	}
	if r == nil {
		return text("You haven't submitted an end-of-day report for **" + s.Teams.TeamName(c.TeamID) +
			"** today. It opens when you check out, or run `/att eod submit`."), nil
	}
	msg, err := s.renderEODForPost(ctx, *r)
	if err != nil {
		return Reply{}, err
	}
	return text(fmt.Sprintf("#### Your end-of-day report — %s, %s\n%s\n\n_%s · Submitted %s. Run `/att eod submit` to change it._",
		s.Teams.TeamName(c.TeamID), s.dayStart(today).Format("Mon 2 Jan"), msg, syncLine(*r), s.fmtClock(r.UpdatedAt))), nil
}

// eodButton is the personal "📝 End-of-day report" button shown after
// check-out, so a closed form can be reopened.
func (s *Service) eodButton(userID, teamID, day string) []mattermost.Attachment {
	if s.Signer == nil || s.ActionURL == "" {
		return nil
	}
	return []mattermost.Attachment{{Actions: []mattermost.Action{{
		ID: "atteod", Name: "📝 End-of-day report", Style: "primary",
		Integration: mattermost.Integration{
			URL:     s.ActionURL,
			Context: s.Signer.Sign(map[string]string{"a": ActEOD, "u": userID, "t": teamID, "d": day}),
		},
	}}}}
}

// eodClick opens the form from the button.
func (s *Service) eodClick(ctx context.Context, c Click) (Reply, error) {
	teamID, day := c.Fields["t"], c.Fields["d"]
	if teamID == "" || !dayRe.MatchString(day) {
		return Reply{Text: "This button is invalid. Run `/att eod submit`.", EphemeralOnly: true}, nil
	}
	if !s.eodDayOpen(day) {
		return Reply{Text: s.eodClosedMsg(day), EphemeralOnly: true}, nil
	}
	if err := s.openEODForm(ctx, c.UserID, teamID, day, c.TriggerID); err != nil {
		s.Log.Warn("eod: open form from button failed", "user_id", c.UserID, "err", err)
		return Reply{Text: "Couldn't open the form. Run `/att eod submit`.", EphemeralOnly: true}, nil
	}
	return Reply{EphemeralOnly: true}, nil
}

// eodDayOpen reports whether a report for day can still be sent or changed:
// today's, or yesterday's until eodLateUntil. Signed forms and buttons live
// longer than that, so this is checked again on use.
func (s *Service) eodDayOpen(day string) bool {
	now := s.Now().In(s.Loc)
	today, _ := DayBounds(now, s.Loc)
	switch day {
	case today.Format(DayFormat):
		return true
	case today.AddDate(0, 0, -1).Format(DayFormat):
		return now.Hour() < eodLateUntil
	}
	return false
}

func (s *Service) eodClosedMsg(day string) string {
	return fmt.Sprintf("The end-of-day report for %s is closed: reports can be sent the same day, or until %d:00 the next morning.",
		s.dayStart(day).Format("Mon 2 Jan"), eodLateUntil)
}
