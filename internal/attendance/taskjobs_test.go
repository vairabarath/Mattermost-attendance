package attendance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeTeamTasks struct {
	tasks   []Task
	err     error
	active  map[int]bool // issue number → branch has commits
	since   time.Time
	calls   int
	closed  map[string]int
	gotFrom time.Time
	gotTo   time.Time
}

func (f *fakeTeamTasks) Configured(team string) bool { return team == "A" }
func (f *fakeTeamTasks) ProjectTasks(context.Context, string) ([]Task, error) {
	f.calls++
	return f.tasks, f.err
}
func (f *fakeTeamTasks) BranchActive(_ context.Context, _, _ string, n int, since time.Time) (bool, error) {
	f.since = since
	return f.active[n], nil
}
func (f *fakeTeamTasks) ClosedCounts(_ context.Context, _ string, logins []string, from, to time.Time) (map[string]int, error) {
	f.gotFrom, f.gotTo = from, to
	return f.closed, f.err
}

func ghTask(n int, status, due string, updated time.Time, assignees ...string) Task {
	t := orgTask(n, status)
	t.Due, t.Updated, t.Assignees = due, updated, assignees
	return t
}

func TestEODReminders(t *testing.T) {
	st := newFakeJobStore()
	day := func(h int) time.Time { return istAt(2026, 11, 25, h, 0) }
	out := func(h int) *time.Time { o := day(h); return &o }
	st.sessions = map[string][]Session{
		"sent":      {{TeamID: "A", CheckIn: day(9), CheckOut: out(17)}},
		"missing":   {{TeamID: "A", CheckIn: day(9), CheckOut: out(17)}},
		"two":       {{TeamID: "A", CheckIn: day(9), CheckOut: out(12)}, {TeamID: "B", CheckIn: day(13), CheckOut: out(18)}},
		"still":     {{TeamID: "A", CheckIn: day(9)}},
		"untracked": {{TeamID: "A", CheckIn: day(9), CheckOut: out(17)}},
		"yesterday": {{TeamID: "A", CheckIn: istAt(2026, 11, 24, 22, 0), CheckOut: out(1)}},
	}
	st.members["untracked"] = MemberSettings{UserID: "untracked", Tracked: false}
	st.eod = []EODStatus{{UserID: "sent", TeamID: "A", Day: "2026-11-25"}}
	j, msg := newJobs(st, nil)

	j.EODReminders(context.Background(), day(19))
	j.EODReminders(context.Background(), day(19)) // restart: not again
	if len(msg.dms) != 2 || len(msg.dms["missing"]) != 1 || len(msg.dms["two"]) != 1 {
		t.Fatalf("dms: %v", msg.dms)
	}
	if !strings.Contains(msg.dms["missing"][0], "for **InkYank** yet") ||
		!strings.Contains(msg.dms["two"][0], "**InkYank**, **Seechanlabs**") || !strings.Contains(msg.dms["two"][0], "/att eod submit") {
		t.Errorf("text: %v", msg.dms)
	}

	// Not on a day off.
	j2, msg2 := newJobs(st, nil)
	j2.EODReminders(context.Background(), istAt(2026, 11, 29, 19, 30)) // Saturday
	if len(msg2.dms) != 0 {
		t.Errorf("weekend: %v", msg2.dms)
	}
}

func TestStaleCutoff(t *testing.T) {
	wd := map[time.Weekday]bool{time.Monday: true, time.Tuesday: true, time.Wednesday: true, time.Thursday: true, time.Friday: true}
	// Wed 25 Nov: Tue 24, Mon 23, (weekend), Fri 20 → since Fri 20 00:00.
	if got := staleCutoff(istAt(2026, 11, 25, 10, 0), "A", wd, nil); !got.Equal(istAt(2026, 11, 20, 0, 0)) {
		t.Errorf("got %v", got)
	}
	// A holiday on Mon 23 pushes it back a day.
	if got := staleCutoff(istAt(2026, 11, 25, 10, 0), "A", wd, []Holiday{{Day: "2026-11-23"}}); !got.Equal(istAt(2026, 11, 19, 0, 0)) {
		t.Errorf("with holiday: %v", got)
	}
}

func TestTaskAlerts(t *testing.T) {
	githubRetryDelays = []time.Duration{0}
	defer func() { githubRetryDelays = []time.Duration{30 * time.Second, 2 * time.Minute} }()
	old := istAt(2026, 11, 18, 12, 0) // before the Fri 20 cutoff
	recent := istAt(2026, 11, 24, 12, 0)
	roster := fakeRoster{"A": {{ID: "u1", Username: "jana"}, {ID: "u2", Username: "barath"}, {ID: "u3", Username: "ravi"}, {ID: "u4", Username: "nogit"}}}
	st := newFakeJobStore()
	for id, login := range map[string]string{"u1": "Jana-Dev", "u2": "vairabarath", "u3": "ravi-gh"} {
		st.members[id] = MemberSettings{UserID: id, Tracked: true, GitHubLogin: login}
	}
	gh := &fakeTeamTasks{active: map[int]bool{12: true}, tasks: []Task{
		ghTask(10, StatusTodo, "2026-11-20", recent, "jana-dev"),               // overdue
		ghTask(11, StatusInProgress, "", old, "jana-dev", "vairabarath"),       // stale, two people
		ghTask(12, StatusInProgress, "", old, "vairabarath"),                   // old, but commits on 12-…
		ghTask(13, StatusInProgress, "", recent, "vairabarath"),                // recently updated
		ghTask(14, StatusTodo, "2026-11-01", old, "someone-not-in-mattermost"), // not a linked member
		ghTask(15, StatusBlocked, "2026-12-30", old, "jana-dev"),               // not overdue, not In progress
	}}
	j, msg := newJobs(st, roster)
	j.GitHub = gh
	wed := istAt(2026, 11, 25, 10, 0)

	j.TaskAlerts(context.Background(), wed)
	j.TaskAlerts(context.Background(), wed) // once per team per day
	if len(msg.posts["admA"]) != 1 || len(msg.posts["admB"]) != 0 {
		t.Fatalf("posts: %v", msg.posts)
	}
	got := msg.posts["admA"][0]
	for _, want := range []string{
		"#### 🗂️ Task check — InkYank — Wed 25 Nov",
		"**⚠️ Overdue (1)**\n- @jana: [#10 Task 10](https://github.com/inkyank-org/app/issues/10) — due 20 Nov",
		"**🐢 Stale (1)** — In progress, no comments, updates or branch commits since Fri 20 Nov",
		"- @barath: [#11 Task 11]", "- @jana: [#11 Task 11]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	// ravi (linked, no tasks) is listed every day; nogit (unlinked) never.
	if !strings.Contains(got, "**😴 No open tasks:** @ravi") {
		t.Errorf("no-tasks section missing:\n%s", got)
	}
	for _, not := range []string{"#12", "#13", "#14", "#15", "nogit", "@jana,"} {
		if strings.Contains(got, not) {
			t.Errorf("unexpected %q in\n%s", not, got)
		}
	}
	if !gh.since.Equal(istAt(2026, 11, 20, 0, 0)) {
		t.Errorf("branch check since %v", gh.since)
	}

	// Every linked member without tasks is listed (any weekday).
	st2 := newFakeJobStore()
	st2.members = st.members
	j2, msg2 := newJobs(st2, roster)
	j2.GitHub = &fakeTeamTasks{tasks: []Task{ghTask(13, StatusInProgress, "", istAt(2026, 11, 29, 12, 0), "vairabarath")}}
	j2.TaskAlerts(context.Background(), istAt(2026, 11, 30, 10, 0))
	if len(msg2.posts["admA"]) != 1 || !strings.Contains(msg2.posts["admA"][0], "**😴 No open tasks:** @jana, @ravi") {
		t.Errorf("monday: %v", msg2.posts)
	}

	// All fine: nothing posted. GitHub down: retried, nothing posted.
	st3 := newFakeJobStore()
	st3.members = map[string]MemberSettings{"u2": st.members["u2"]}
	j3, msg3 := newJobs(st3, roster)
	j3.GitHub = &fakeTeamTasks{tasks: []Task{ghTask(13, StatusInProgress, "", recent, "vairabarath")}}
	j3.TaskAlerts(context.Background(), wed)
	down := &fakeTeamTasks{err: errors.New("502")}
	j4, msg4 := newJobs(newFakeJobStore(), roster)
	j4.Store.(*fakeJobStore).members = st.members
	j4.GitHub = down
	j4.TaskAlerts(context.Background(), wed)
	if len(msg3.posts)+len(msg4.posts) != 0 || down.calls != 2 {
		t.Errorf("quiet day / github down: %v %v calls=%d", msg3.posts, msg4.posts, down.calls)
	}
}

func TestDailySummaryEODColumns(t *testing.T) {
	st := newFakeJobStore()
	day := func(h int) time.Time { return istAt(2026, 11, 25, h, 0) }
	o := day(17)
	st.sessions = map[string][]Session{
		"u1": {{TeamID: "A", CheckIn: day(9), CheckOut: &o, WorkMode: "office"}},
		"u2": {{TeamID: "A", CheckIn: day(9), CheckOut: &o, WorkMode: "office"}},
		"u3": {{TeamID: "A", CheckIn: day(10), WorkMode: "wfh"}},
	}
	st.eod = []EODStatus{{UserID: "u1", TeamID: "A", Day: "2026-11-25", Done: 2, Blocked: 1}}
	st.roots = map[string]string{"A/2026-11-25": "root1"}
	roster := fakeRoster{"A": {{ID: "u1", Username: "jana"}, {ID: "u2", Username: "barath"}, {ID: "u3", Username: "ravi"}}}
	j, msg := newJobs(st, roster)
	j.Links = fakeLinks{}
	j.DailySummaries(context.Background(), day(20))
	got := msg.posts["chA"][0]
	for _, want := range []string{
		"| Member | Hours | Mode | EOD | Done | Blocked |",
		"| @jana | 8h 0m | 🏢 | ✅ | 2 | 1 🚧 |",
		"| @barath | 8h 0m | 🏢 | ❌ | | |",
		"| @ravi | 10h 0m | 🏠 | ⏳ | | |",
		"📝 [Today's end-of-day reports](https://chat/A/pl/root1)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

type fakeLinks struct{}

func (fakeLinks) Permalink(_ context.Context, team, post string) (string, error) {
	return "https://chat/" + team + "/pl/" + post, nil
}

func TestWeeklyTasksTable(t *testing.T) {
	rows := []TeamReportRow{
		{UserID: "u1", Username: "jana", Days: 5},
		{UserID: "u2", Username: "barath", Days: 4},
		{UserID: "u3", Username: "ravi"},           // linked, didn't work
		{UserID: "u4", Username: "away", Days: 0},  // unlinked, didn't work: left out
		{UserID: "u5", Username: "nogit", Days: 3}, // unlinked
	}
	in := WeeklyTaskInput{
		Rows: rows, Reports: map[string]int{"u1": 5, "u2": 3, "u5": 1},
		Logins:  map[string]string{"u1": "Jana-Dev", "u2": "vairabarath", "u3": "ravi-gh"},
		Closed:  map[string]int{"jana-dev": 4, "vairabarath": 2},
		Overdue: map[string]int{"vairabarath": 1},
	}
	got := RenderWeeklyTasks(in)
	for _, want := range []string{
		"| @jana | 5/5 | 4 | 0 |", "| @barath | 3/4 | 2 | 1 ⚠️ |", "| @ravi | — | 0 | 0 |", "| @nogit | 1/3 | — | — |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "@away") {
		t.Error("unlinked member with no days listed")
	}
	in.GitHubErr = true
	if got := RenderWeeklyTasks(in); !strings.Contains(got, "| @jana | 5/5 | — | — |") || !strings.Contains(got, "GitHub couldn't be read") {
		t.Errorf("github error:\n%s", got)
	}
}

func TestWeeklyReportHasTaskTable(t *testing.T) {
	st := newFakeJobStore()
	st.members["u1"] = MemberSettings{UserID: "u1", Username: "jana", Tracked: true, ExpectedHours: 8, GitHubLogin: "Jana-Dev"}
	in := istAt(2026, 11, 24, 9, 0)
	o := istAt(2026, 11, 24, 17, 0)
	st.sessions = map[string][]Session{"u1": {{TeamID: "A", CheckIn: in, CheckOut: &o}}}
	st.eod = []EODStatus{{UserID: "u1", TeamID: "A", Day: "2026-11-24"}}
	gh := &fakeTeamTasks{closed: map[string]int{"Jana-Dev": 3}}
	j, msg := newJobs(st, fakeRoster{"A": {{ID: "u1", Username: "jana"}}})
	j.GitHub = gh
	j.Now = func() time.Time { return istAt(2026, 11, 30, 9, 0) }
	j.WeeklyReports(context.Background(), istAt(2026, 11, 30, 9, 0))
	got := msg.posts["admA"][0]
	if !strings.Contains(got, "**Tasks and end-of-day reports**") || !strings.Contains(got, "| @jana | 1/1 | 3 | 0 |") {
		t.Errorf("weekly:\n%s", got)
	}
	// Last week, Mon 00:00 to Sun 23:59:59 local.
	if !gh.gotFrom.Equal(istAt(2026, 11, 23, 0, 0)) || !gh.gotTo.Equal(istAt(2026, 11, 29, 23, 59).Add(59*time.Second)) {
		t.Errorf("range %v – %v", gh.gotFrom, gh.gotTo)
	}
	// Team B has no GitHub: no issue columns, EOD only.
	if strings.Contains(msg.posts["admB"][0], "Tasks and end-of-day") {
		t.Errorf("team B: %s", msg.posts["admB"][0])
	}
}
