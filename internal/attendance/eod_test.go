package attendance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/vairabarath/Mattermost-attendance/internal/config"
	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

func task(n int, status string, updatedHour int) Task {
	return Task{Repo: "o/app", Number: n, Title: fmt.Sprintf("Task %d", n), Status: status,
		URL:     fmt.Sprintf("https://github.com/o/app/issues/%d", n),
		Updated: time.Date(2026, 11, 26, updatedHour, 0, 0, 0, time.UTC)}
}

func TestRelevantIssues(t *testing.T) {
	open := []Task{
		task(1, StatusInProgress, 1),
		task(2, StatusTodo, 2),    // not active, not picked: left out
		task(3, StatusBlocked, 3), // active
		task(1, StatusInProgress, 1),
		task(4, StatusTodo, 4),        // picked today
		task(5, StatusInReview, 5),    // active
		task(42, StatusInProgress, 6), // also closed today: "already done", not a dropdown
	}
	closed := []Task{task(42, StatusDone, 7), task(42, StatusDone, 7)}
	sel, done, omitted := RelevantIssues(open, closed, map[string]bool{task(4, "", 0).URL: true})
	var got []int
	for _, s := range sel {
		got = append(got, s.Number)
	}
	if fmt.Sprint(got) != "[5 4 3 1]" || omitted != 0 { // most recently updated first
		t.Errorf("selects %v omitted %d", got, omitted)
	}
	if len(done) != 1 || done[0].Number != 42 {
		t.Errorf("done %+v", done)
	}

	var many []Task
	for i := 1; i <= 11; i++ {
		many = append(many, task(i, StatusInProgress, i))
	}
	sel, _, omitted = RelevantIssues(many, nil, nil)
	if len(sel) != maxEODSelects || omitted != 3 || sel[0].Number != 11 || sel[7].Number != 4 {
		t.Errorf("limit: %d selects, %d omitted, first #%d last #%d", len(sel), omitted, sel[0].Number, sel[len(sel)-1].Number)
	}
}

func TestRenderEOD(t *testing.T) {
	r := EODReport{
		DidToday: "Finished the timeout handling @channel", Blockers: "", Tomorrow: "CSV export",
		Items: []EODItem{
			{IssueURL: "https://github.com/o/app/issues/45", Title: "WFH buttons", OldStatus: StatusTodo, NewStatus: StatusInProgress},
			{IssueURL: "https://github.com/o/app/issues/42", Title: "Session [timeout]", NewStatus: StatusDone},
			{IssueURL: "https://github.com/o/ops/issues/47", Title: "Export CSV", OldStatus: StatusInProgress, NewStatus: StatusBlocked},
		},
	}
	got := RenderEOD("Jana", 7*time.Hour+40*time.Minute, map[string]bool{"office": true}, r)
	want := "📝 **Jana** — 7h 40m (🏢 office)\n" +
		"✅ Done: [#42](https://github.com/o/app/issues/42) Session \\[timeout\\]\n" +
		"🔄 In progress: [#45](https://github.com/o/app/issues/45) WFH buttons\n" +
		"🚧 Blocked: [#47](https://github.com/o/ops/issues/47) Export CSV\n" +
		"**Did:** Finished the timeout handling @​channel\n" +
		"**Tomorrow:** CSV export"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := RenderEOD("Raj", time.Hour, map[string]bool{"wfh": true}, EODReport{DidToday: "Docs"}); got != "📝 **Raj** — 1h 0m (🏠 WFH)\n**Did:** Docs" {
		t.Errorf("minimal: %q", got)
	}
}

// --- service-level ---

type eodStore struct {
	Store
	member   MemberSettings
	sessions []Session
	reports  map[string]*EODReport // user/team/day
	threads  map[string]string
	nextID   int64
	picks    map[string][]string // user/team/day → urls
}

func (f *eodStore) AddTaskPick(_ context.Context, u, t, d, url string) error {
	if f.picks == nil {
		f.picks = map[string][]string{}
	}
	k := u + "/" + t + "/" + d
	if !slices.Contains(f.picks[k], url) {
		f.picks[k] = append(f.picks[k], url)
	}
	return nil
}
func (f *eodStore) TaskPicks(_ context.Context, u, t, d string) ([]string, error) {
	return f.picks[u+"/"+t+"/"+d], nil
}
func (f *eodStore) OpenSession(context.Context, string) (*Session, error) {
	if n := len(f.sessions); n > 0 && f.sessions[n-1].CheckOut == nil {
		s := f.sessions[n-1]
		return &s, nil
	}
	return nil, nil
}
func (f *eodStore) WFHApprovedOn(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (f *eodStore) InsertSession(_ context.Context, _, team string, in time.Time, mode, _ string) (int64, error) {
	if s, _ := f.OpenSession(context.Background(), ""); s != nil {
		return 0, ErrAlreadyCheckedIn
	}
	f.sessions = append(f.sessions, Session{ID: int64(len(f.sessions) + 1), TeamID: team, CheckIn: in, WorkMode: mode})
	return int64(len(f.sessions)), nil
}

func (f *eodStore) MemberRefreshedAt(context.Context, string) (time.Time, bool, error) {
	return time.Now(), true, nil
}
func (f *eodStore) GetMember(context.Context, string) (*MemberSettings, error) {
	m := f.member
	return &m, nil
}
func (f *eodStore) SessionsOverlapping(_ context.Context, _ string, from, to time.Time) ([]Session, error) {
	var out []Session
	for _, s := range f.sessions {
		if s.CheckIn.Before(to) && (s.CheckOut == nil || s.CheckOut.After(from)) {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *eodStore) CloseOpenSession(_ context.Context, _ string, out time.Time, _ string) (*Session, error) {
	s := f.sessions[len(f.sessions)-1]
	s.CheckOut = &out
	f.sessions[len(f.sessions)-1] = s
	return &s, nil
}
func (f *eodStore) SaveEODReport(_ context.Context, r EODReport) (EODReport, error) {
	k := r.UserID + "/" + r.TeamID + "/" + r.Day
	if old, ok := f.reports[k]; ok {
		r.ID, r.ReplyPostID, r.BlockerPostID, r.Activity = old.ID, old.ReplyPostID, old.BlockerPostID, old.Activity
	} else {
		f.nextID++
		r.ID = f.nextID
	}
	c := r
	f.reports[k] = &c
	return r, nil
}
func (f *eodStore) GetEODReport(_ context.Context, u, t, d string) (*EODReport, error) {
	if r, ok := f.reports[u+"/"+t+"/"+d]; ok {
		c := *r
		return &c, nil
	}
	return nil, nil
}
func (f *eodStore) EnsureEODThread(_ context.Context, team, day string, create func() (string, error)) (string, error) {
	if id, ok := f.threads[team+day]; ok {
		return id, nil
	}
	id, err := create()
	if err == nil {
		f.threads[team+day] = id
	}
	return id, err
}
func (f *eodStore) byID(id int64) *EODReport {
	for _, r := range f.reports {
		if r.ID == id {
			return r
		}
	}
	return nil
}
func (f *eodStore) EnsureEODReply(_ context.Context, id int64, create func() (string, error)) (string, bool, error) {
	r := f.byID(id)
	if r == nil {
		return "", false, errors.New("no report")
	}
	if r.ReplyPostID != "" {
		return r.ReplyPostID, false, nil
	}
	pid, err := create()
	r.ReplyPostID = pid
	return pid, true, err
}
func (f *eodStore) EnsureEODBlockerPost(_ context.Context, id int64, create func() (string, error)) (string, bool, error) {
	r := f.byID(id)
	if r.BlockerPostID != "" {
		return r.BlockerPostID, false, nil
	}
	pid, err := create()
	r.BlockerPostID = pid
	return pid, true, err
}
func (f *eodStore) GetEODReportByID(_ context.Context, id int64) (*EODReport, error) {
	if r := f.byID(id); r != nil {
		c := *r
		c.Items = append([]EODItem(nil), r.Items...)
		return &c, nil
	}
	return nil, nil
}
func (f *eodStore) ClaimEODSync(_ context.Context, id int64, _ time.Duration) (*EODReport, error) {
	r := f.byID(id)
	if r == nil || r.SyncStatus == "synced" {
		return nil, nil
	}
	r.SyncAttempts++
	c := *r
	c.Items = append([]EODItem(nil), r.Items...)
	return &c, nil
}
func (f *eodStore) MarkEODItemSynced(_ context.Context, id int64, url string) error {
	r := f.byID(id)
	for i := range r.Items {
		if r.Items[i].IssueURL == url {
			r.Items[i].Synced = true
		}
	}
	return nil
}
func (f *eodStore) FinishEODSync(_ context.Context, id int64, status, syncErr string, act *Activity) error {
	r := f.byID(id)
	r.SyncStatus, r.SyncError = status, syncErr
	if act != nil {
		r.Activity = act
	}
	return nil
}
func (f *eodStore) PendingEODSyncs(context.Context, int, time.Time) ([]int64, error) {
	var ids []int64
	for _, r := range f.reports {
		if r.SyncStatus != "synced" && r.SyncAttempts < maxSyncAttempts {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}

type eodPoster struct {
	fakePoster
	dialogs   []mattermost.Dialog
	roots     []string // posts without a thread (EOD roots and admin alerts)
	replies   []string
	updates   []string
	ephemeral []string
}

func (p *eodPoster) OpenDialog(_ context.Context, _, _ string, d mattermost.Dialog) error {
	p.dialogs = append(p.dialogs, d)
	return nil
}
func (p *eodPoster) CreatePostWithButtons(_ context.Context, _, msg string, _ []mattermost.Attachment) (string, error) {
	p.roots = append(p.roots, msg)
	return fmt.Sprintf("root%d", len(p.roots)), nil
}
func (p *eodPoster) CreateReply(_ context.Context, _, _, msg string) (string, error) {
	p.replies = append(p.replies, msg)
	return fmt.Sprintf("reply%d", len(p.replies)), nil
}
func (p *eodPoster) UpdatePost(_ context.Context, _, msg string, _ []mattermost.Attachment) error {
	p.updates = append(p.updates, msg)
	return nil
}
func (p *eodPoster) EphemeralPost(_ context.Context, _, _, msg string) error {
	p.ephemeral = append(p.ephemeral, msg)
	return nil
}

func newEODService(now time.Time) (*Service, *eodStore, *eodPoster, *fakeTasks) {
	in := istAt(2026, 11, 26, 10, 0)
	st := &eodStore{
		member:   MemberSettings{UserID: "u", Username: "jana", DisplayName: "Jana", GitHubLogin: "Jana-Dev"},
		sessions: []Session{{ID: 1, TeamID: "A", CheckIn: in, WorkMode: "office"}},
		reports:  map[string]*EODReport{}, threads: map[string]string{},
	}
	ft := &fakeTasks{configured: map[string]bool{"A": true}}
	p := &eodPoster{}
	svc := &Service{
		Store: st, Teams: teamNames{"A": "InkYank", "B": "Seechanlabs"}, Tasks: ft, Posts: p,
		Signer: mattermost.NewSigner("k", time.Hour), ActionURL: "http://x/mm/action", DialogURL: "http://x/mm/dialog",
		TeamCfg: map[string]config.Team{"A": {ID: "A", AttendanceChannelID: "chanA"}},
		Loc:     ist, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return now },
	}
	return svc, st, p, ft
}

func TestCheckOutOpensEODForm(t *testing.T) {
	svc, _, p, ft := newEODService(istAt(2026, 11, 26, 18, 0))
	ft.eodOpen = []Task{task(45, StatusInProgress, 9), task(46, StatusTodo, 9)}
	ft.eodClosed = []Task{task(42, StatusDone, 11)}

	r := svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", TriggerID: "trig", Text: "out"})
	if !strings.Contains(r.Text, "Checked out") || len(r.Attachments) != 1 || r.Attachments[0].Actions[0].Name != "📝 End-of-day report" {
		t.Fatalf("reply: %+v", r)
	}
	if len(p.dialogs) != 1 {
		t.Fatalf("form not opened")
	}
	d := p.dialogs[0]
	if d.Title != "End of day report" || !strings.Contains(d.IntroductionText, "✅ Already done today: #42") {
		t.Errorf("dialog: %q / %q", d.Title, d.IntroductionText)
	}
	// #45 (In progress) gets a dropdown; #46 (Todo) is offered under
	// "Other tasks I finished".
	if len(d.Elements) != 5 || d.Elements[0].Type != "select" || d.Elements[0].DisplayName != "#45 Task 45" ||
		d.Elements[0].Default != noChange || d.Elements[1].Name != "others" || !d.Elements[1].MultiSelect ||
		len(d.Elements[1].Options) != 1 || d.Elements[1].Options[0].Text != "#46 Task 46" ||
		d.Elements[2].Name != "did" || d.Elements[2].MinLength != minDidLen {
		t.Errorf("elements: %+v", d.Elements)
	}
	for _, e := range d.Elements {
		if utf8.RuneCountInString(e.DisplayName) > 24 || utf8.RuneCountInString(e.HelpText) > 150 {
			t.Errorf("element %q over Mattermost's limits", e.Name)
		}
	}
	if len(d.Title) > 24 {
		t.Error("title over 24 characters")
	}

	// The button reopens the form, bound to the session's team and day.
	ctx := r.Attachments[0].Actions[0].Integration.Context
	if ctx["a"] != ActEOD || ctx["t"] != "A" || ctx["d"] != "2026-11-26" || ctx["u"] != "u" {
		t.Errorf("button context %v", ctx)
	}
	res := svc.HandleButton(context.Background(), Click{Action: ActEOD, UserID: "u", Fields: ctx, TriggerID: "t2"})
	if !res.EphemeralOnly || len(p.dialogs) != 2 {
		t.Errorf("button: %+v, %d dialogs", res, len(p.dialogs))
	}
}

func TestEODFormWithoutTasks(t *testing.T) {
	// GitHub failing, no login, or no GitHub for the team: text-only form.
	for name, setup := range map[string]func(*eodStore, *fakeTasks){
		"github error": func(_ *eodStore, ft *fakeTasks) { ft.eodErr = errors.New("timeout") },
		"no login":     func(st *eodStore, _ *fakeTasks) { st.member.GitHubLogin = "" },
		"no github":    func(_ *eodStore, ft *fakeTasks) { ft.configured = nil },
	} {
		svc, st, p, ft := newEODService(istAt(2026, 11, 26, 18, 0))
		ft.eodOpen = []Task{task(45, StatusInProgress, 9)}
		setup(st, ft)
		svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", TriggerID: "trig", Text: "out"})
		if len(p.dialogs) != 1 || len(p.dialogs[0].Elements) != 3 || p.dialogs[0].Elements[0].Name != "did" {
			t.Errorf("%s: %+v", name, p.dialogs)
		}
	}
}

// submit fills in the last opened form and submits it.
func submit(t *testing.T, svc *Service, d mattermost.Dialog, values map[string]any) DialogResult {
	t.Helper()
	var state map[string]string
	if err := json.Unmarshal([]byte(d.State), &state); err != nil {
		t.Fatal(err)
	}
	fields, err := mattermost.NewSigner("k", time.Hour).Verify(state)
	if err != nil {
		t.Fatal(err)
	}
	return svc.HandleDialog(context.Background(), DialogSubmit{Fields: fields, UserID: "u", ChannelID: "c", Submission: values})
}

func TestSubmitEODAndResubmit(t *testing.T) {
	svc, st, p, ft := newEODService(istAt(2026, 11, 26, 18, 0))
	ft.eodOpen = []Task{task(45, StatusInProgress, 9), task(47, StatusInProgress, 8)}
	ft.eodClosed = []Task{task(42, StatusDone, 11)}
	svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", TriggerID: "trig", Text: "out"})
	form := p.dialogs[0]

	if res := submit(t, svc, form, map[string]any{"did": "short"}); res.Errors["did"] == "" {
		t.Fatalf("short 'did' accepted: %+v", res)
	}
	if res := submit(t, svc, form, map[string]any{"did": "Worked on things", "s0": "Shipped"}); res.Errors["s0"] == "" {
		t.Errorf("unknown status accepted: %+v", res)
	}
	res := submit(t, svc, form, map[string]any{"did": "Finished the timeout handling", "s0": StatusDone, "s1": noChange, "blockers": ""})
	if res.Error != "" || len(res.Errors) != 0 {
		t.Fatalf("submit: %+v", res)
	}
	r := st.reports["u/A/2026-11-26"]
	if r == nil || len(r.Items) != 3 {
		t.Fatalf("saved: %+v", r)
	}
	byURL := map[string]EODItem{}
	for _, it := range r.Items {
		byURL[it.IssueURL] = it
	}
	if it := byURL[task(45, "", 0).URL]; it.NewStatus != StatusDone || it.OldStatus != StatusInProgress || it.Synced {
		t.Errorf("#45 changed → needs sync: %+v", it)
	}
	if it := byURL[task(47, "", 0).URL]; it.NewStatus != StatusInProgress || !it.Synced {
		t.Errorf("#47 unchanged: %+v", it)
	}
	if it := byURL[task(42, "", 0).URL]; it.NewStatus != StatusDone || !it.Synced {
		t.Errorf("#42 closed today: recorded as Done with no GitHub write: %+v", it)
	}
	if len(p.roots) != 1 || !strings.Contains(p.roots[0], "EOD reports — Thu 26 Nov") || len(p.replies) != 1 ||
		!strings.Contains(p.replies[0], "📝 **Jana** — 8h 0m (🏢 office)") || !strings.Contains(p.replies[0], "✅ Done: [#42]") {
		t.Errorf("posted: roots %q replies %q", p.roots, p.replies)
	}
	if len(p.ephemeral) != 1 || !strings.Contains(p.ephemeral[0], "saved and posted") {
		t.Errorf("confirmation: %q", p.ephemeral)
	}

	// Resubmitting: the form is pre-filled, and the same reply is edited.
	svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", TriggerID: "trig2", Text: "eod submit"})
	form2 := p.dialogs[len(p.dialogs)-1]
	var did string
	for _, e := range form2.Elements {
		if e.Name == "did" {
			did = e.Default
		}
	}
	if did != "Finished the timeout handling" || form2.Elements[0].Default != StatusDone ||
		!strings.Contains(form2.IntroductionText, "submitting again updates it") {
		t.Errorf("prefill: did=%q s0=%q intro=%q", did, form2.Elements[0].Default, form2.IntroductionText)
	}
	submit(t, svc, form2, map[string]any{"did": "Finished timeout handling and tests", "s0": StatusDone, "s1": StatusBlocked, "blockers": "Waiting for DB access"})
	if len(p.roots) != 1 || len(p.replies) != 1 || len(p.updates) != 1 {
		t.Fatalf("resubmit should edit the reply: roots %d replies %d updates %d", len(p.roots), len(p.replies), len(p.updates))
	}
	if !strings.Contains(p.updates[0], "🚧 Blocked: [#47]") || !strings.Contains(p.updates[0], "**Blockers:** Waiting for DB access") {
		t.Errorf("updated reply: %s", p.updates[0])
	}
	if len(st.reports) != 1 {
		t.Errorf("reports: %d (should update, not duplicate)", len(st.reports))
	}

	if out := svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", Text: "eod"}).Text; !strings.Contains(out, "Your end-of-day report — InkYank") ||
		!strings.Contains(out, "Finished timeout handling and tests") {
		t.Errorf("/att eod: %q", out)
	}
}

func TestEODDayAndCommands(t *testing.T) {
	// Before 11:00 with no work today: yesterday's report.
	svc, st, p, _ := newEODService(istAt(2026, 11, 27, 10, 30))
	out := istAt(2026, 11, 26, 18, 0)
	st.sessions[0].CheckOut = &out
	if out := svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", TriggerID: "t", Text: "eod submit"}).Text; out != "" || len(p.dialogs) != 1 {
		t.Fatalf("late submit: %q", out)
	}
	if !strings.Contains(p.dialogs[0].IntroductionText, "Thu 26 Nov") {
		t.Errorf("should report on yesterday: %q", p.dialogs[0].IntroductionText)
	}
	// After 11:00: too late, and nothing today.
	svc.Now = func() time.Time { return istAt(2026, 11, 27, 11, 0) }
	if out := svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", TriggerID: "t", Text: "eod submit"}).Text; !strings.Contains(out, "haven't worked") {
		t.Errorf("after 11: %q", out)
	}
	// Another team: no work there.
	svc.Now = func() time.Time { return istAt(2026, 11, 26, 18, 0) }
	if out := svc.Handle(context.Background(), Command{TeamID: "B", UserID: "u", TriggerID: "t", Text: "eod submit"}).Text; !strings.Contains(out, "Seechanlabs") {
		t.Errorf("other team: %q", out)
	}
	if out := svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", Text: "eod"}).Text; !strings.Contains(out, "haven't submitted") {
		t.Errorf("/att eod none: %q", out)
	}
	if out := svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", Text: "eod bogus"}).Text; !strings.Contains(out, "Usage") {
		t.Errorf("usage: %q", out)
	}
	_ = st
}

func TestEODNoChannelStillSaves(t *testing.T) {
	svc, st, p, _ := newEODService(istAt(2026, 11, 26, 18, 0))
	svc.TeamCfg = nil
	svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", TriggerID: "trig", Text: "out"})
	submit(t, svc, p.dialogs[0], map[string]any{"did": "Did a full day of work"})
	if st.reports["u/A/2026-11-26"] == nil || len(p.replies) != 0 || !strings.Contains(p.ephemeral[0], "couldn't be posted") {
		t.Errorf("saved=%v replies=%d msg=%q", st.reports["u/A/2026-11-26"] != nil, len(p.replies), p.ephemeral)
	}
}

func svcTeam(attendance, admin string) config.Team {
	return config.Team{ID: "A", AttendanceChannelID: attendance, AdminChannelID: admin}
}
