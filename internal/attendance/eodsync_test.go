package attendance

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBranchIssue(t *testing.T) {
	cases := map[string]int{
		"42-session-timeout": 42, "7-x": 7, "123-": 123,
		"feature-42": 0, "main": 0, "42": 0, "0-zero": 0, "v1.2-release": 0, "-42-x": 0,
	}
	for name, want := range cases {
		if got := BranchIssue(name); got != want {
			t.Errorf("BranchIssue(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestRenderActivity(t *testing.T) {
	a := &Activity{Commits: 6, IssuesClosed: 1, Branches: []BranchWork{
		{Repo: "o/app", Branch: "45-wfh", Issue: 45}, {Repo: "o/app", Branch: "42-timeout", Issue: 42}, {Repo: "o/web", Branch: "42-ui", Issue: 42},
	}}
	if got := RenderActivity(a); got != "GitHub: 6 commits to default branches · 1 issue closed · branches: 42, 45" {
		t.Errorf("got %q", got)
	}
	a = &Activity{Commits: 1, PRsOpened: 1, PRsMerged: 2, Reviews: 3}
	if got := RenderActivity(a); got != "GitHub: 1 commit to default branches · 1 PR opened · 2 PRs merged · 3 reviews" {
		t.Errorf("got %q", got)
	}
	if RenderActivity(&Activity{}) != "" || RenderActivity(nil) != "" {
		t.Error("no activity should render nothing")
	}
}

func TestEODComment(t *testing.T) {
	c := eodComment("2026-11-26", "jana", StatusDone, "Fixed it with @raj's help "+strings.Repeat("x", 400))
	if !strings.HasPrefix(c, "📝 EOD 2026-11-26 (`@jana`): **Done** — Fixed it with @​raj's help") {
		t.Errorf("comment: %q", c[:80])
	}
	if n := len([]rune(c[strings.Index(c, "— ")+4:])); n > commentDidLen+5 {
		t.Errorf("did not clipped: %d runes", n)
	}
}

type fakeWriter struct {
	mu        sync.Mutex
	applied   []string // url → status
	comments  []string
	failURL   string
	notMine   string
	actErr    error
	act       *Activity
	actCalled int

	started             []string // StartTask calls
	closedURL, inReview string
	offProject          string
	onApply             func() // runs inside ApplyEODStatus (e.g. a resubmit mid-sync)
}

func (w *fakeWriter) ApplyEODStatus(_ context.Context, _, login, url, status, comment string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if url == w.notMine {
		return false, ErrNotAssigned
	}
	if url == w.failURL {
		return false, errors.New("github 502")
	}
	w.applied = append(w.applied, issueRef(url)+"="+status)
	w.comments = append(w.comments, comment)
	if f := w.onApply; f != nil {
		w.onApply = nil
		w.mu.Unlock()
		f()
		w.mu.Lock()
	}
	return status == StatusDone, nil
}
func (w *fakeWriter) StartTask(_ context.Context, _, login, url string) (Pick, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.started = append(w.started, issueRef(url))
	switch {
	case url == w.notMine:
		return Pick{}, ErrNotAssigned
	case url == w.failURL:
		return Pick{}, errors.New("github 502")
	case url == w.offProject:
		return Pick{}, ErrNotOnProject
	case url == w.closedURL:
		return Pick{Number: issueNum(url), Title: "Old"}, ErrIssueClosed
	case url == w.inReview:
		return Pick{Number: issueNum(url), Title: "Review me", Status: StatusInReview}, nil
	}
	return Pick{Number: issueNum(url), Title: "Task *" + issueRef(url) + "*", Status: StatusInProgress, Started: true}, nil
}
func (w *fakeWriter) Activity(context.Context, string, string, time.Time) (*Activity, error) {
	w.actCalled++
	return w.act, w.actErr
}

// submitted runs check-out + form submission and returns the report id.
func submitted(t *testing.T, svc *Service, p *eodPoster, values map[string]any) {
	t.Helper()
	svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", TriggerID: "trig", Text: "out"})
	if res := submit(t, svc, p.dialogs[len(p.dialogs)-1], values); res.Error != "" || len(res.Errors) > 0 {
		t.Fatalf("submit: %+v", res)
	}
}

func TestSyncEOD(t *testing.T) {
	svc, st, p, ft := newEODService(istAt(2026, 11, 26, 18, 0))
	w := &fakeWriter{act: &Activity{Commits: 3, Branches: []BranchWork{{Repo: "o/app", Branch: "45-x", Issue: 45}}}}
	svc.Writer = w
	ft.eodOpen = []Task{task(45, StatusInProgress, 9), task(47, StatusInProgress, 8), task(48, StatusInReview, 7), task(50, StatusTodo, 1)}
	ft.eodClosed = []Task{task(42, StatusDone, 11)}
	w.notMine = task(48, "", 0).URL // reassigned since the form opened
	w.failURL = task(47, "", 0).URL // GitHub fails for this one, first time

	submitted(t, svc, p, map[string]any{"did": "Finished the timeout work", "s0": StatusDone, "s1": StatusBlocked, "s2": StatusDone,
		"others": []any{"0"}}) // #50 (Todo) finished too
	r := st.reports["u/A/2026-11-26"]
	if r.SyncStatus != "pending" || !strings.Contains(p.ephemeral[0], "being written to GitHub") {
		t.Fatalf("status %q, confirm %q", r.SyncStatus, p.ephemeral)
	}
	var other *EODItem
	for i := range r.Items {
		if r.Items[i].IssueURL == task(50, "", 0).URL {
			other = &r.Items[i]
		}
	}
	if other == nil || other.NewStatus != StatusDone || other.OldStatus != StatusTodo || other.Synced {
		t.Fatalf("'other task' #50: %+v", other)
	}

	svc.SyncEOD(context.Background(), r.ID) // first attempt: #47 fails
	if r.SyncStatus != "failed" || !strings.Contains(r.SyncError, "#47: github 502") || !strings.Contains(r.SyncError, "#48 skipped: not assigned to you") {
		t.Fatalf("after attempt 1: %q / %q", r.SyncStatus, r.SyncError)
	}
	if got := strings.Join(w.applied, " "); got != "#45=Done #50=Done" {
		t.Errorf("applied %q (unchanged #42 and refused #48 must not be written)", got)
	}
	if !strings.Contains(w.comments[0], "(`@jana`): **Done** — Finished the timeout work") {
		t.Errorf("comment %q", w.comments[0])
	}
	// The reply now carries the GitHub line.
	if len(p.updates) == 0 || !strings.Contains(p.updates[len(p.updates)-1], "GitHub: 3 commits to default branches · 2 issues closed · branch: 45") {
		t.Errorf("reply refresh: %q", p.updates)
	}
	if out := svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", Text: "eod"}).Text; !strings.Contains(out, "sync failed (try 1 of 6") {
		t.Errorf("/att eod should surface the failure: %q", out)
	}

	// The retry job picks it up; only #47 is left and now works.
	w.failURL = ""
	svc.RetryEODSyncs(context.Background())
	if r.SyncStatus != "synced" || strings.Join(w.applied, " ") != "#45=Done #50=Done #47=Blocked" {
		t.Errorf("after retry: %q %q applied=%v", r.SyncStatus, r.SyncError, w.applied)
	}
	if !strings.Contains(r.SyncError, "#48 skipped") {
		t.Errorf("the refused issue stays noted: %q", r.SyncError)
	}
	svc.RetryEODSyncs(context.Background()) // nothing left: no more writes
	if len(w.applied) != 3 {
		t.Errorf("synced report written again: %v", w.applied)
	}
}

func TestSyncEODGivesUpAndAlertsOnce(t *testing.T) {
	svc, st, p, ft := newEODService(istAt(2026, 11, 26, 18, 0))
	svc.TeamCfg["A"] = svcTeam("chanA", "adminA")
	w := &fakeWriter{actErr: errors.New("github down")}
	svc.Writer = w
	ft.eodOpen = []Task{task(45, StatusInProgress, 9)}
	submitted(t, svc, p, map[string]any{"did": "Worked on the report sync"})
	r := st.reports["u/A/2026-11-26"]
	for i := 0; i < maxSyncAttempts+2; i++ {
		svc.RetryEODSyncs(context.Background())
	}
	alerts := 0
	for _, m := range p.roots {
		if strings.Contains(m, "Couldn't sync @jana's end-of-day report") {
			alerts++
		}
	}
	if r.SyncStatus != "failed" || r.SyncAttempts != maxSyncAttempts || alerts != 1 || w.actCalled != maxSyncAttempts {
		t.Errorf("status %q attempts %d alerts %d activity calls %d", r.SyncStatus, r.SyncAttempts, alerts, w.actCalled)
	}
}

func TestNoGitHubMeansSynced(t *testing.T) {
	svc, st, p, _ := newEODService(istAt(2026, 11, 26, 18, 0))
	st.member.GitHubLogin = ""
	svc.Writer = &fakeWriter{}
	submitted(t, svc, p, map[string]any{"did": "Text-only report today"})
	if r := st.reports["u/A/2026-11-26"]; r.SyncStatus != "synced" || strings.Contains(p.ephemeral[0], "GitHub") {
		t.Errorf("status %q confirm %q", r.SyncStatus, p.ephemeral)
	}
}

func TestBlockerAlerts(t *testing.T) {
	svc, st, p, ft := newEODService(istAt(2026, 11, 26, 18, 0))
	svc.TeamCfg["A"] = svcTeam("chanA", "adminA")
	ft.eodOpen = []Task{task(47, StatusInProgress, 9)}

	submitted(t, svc, p, map[string]any{"did": "No blockers today at all"})
	for _, m := range p.roots {
		if strings.Contains(m, "blocker") {
			t.Fatalf("alert without blockers: %q", m)
		}
	}
	// Resubmit with a Blocked task and a blocker: one alert with a link.
	svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", TriggerID: "t", Text: "eod submit"})
	submit(t, svc, p.dialogs[len(p.dialogs)-1], map[string]any{"did": "Waiting on access", "s0": StatusBlocked, "blockers": "Need DB access @channel"})
	r := st.reports["u/A/2026-11-26"]
	var alert string
	for _, m := range p.roots {
		if strings.Contains(m, "reported a blocker") {
			alert = m
		}
	}
	if r.BlockerPostID == "" || !strings.Contains(alert, "**@jana** reported a blocker (Thu 26 Nov) — [view report](https://chat.example/A/pl/reply1)") ||
		!strings.Contains(alert, "> Need DB access @​channel") || !strings.Contains(alert, "Blocked: [#47]") {
		t.Fatalf("alert: %q", alert)
	}
	// Resubmitting again edits the same alert; clearing the blockers marks it resolved.
	roots := len(p.roots)
	svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", TriggerID: "t", Text: "eod submit"})
	submit(t, svc, p.dialogs[len(p.dialogs)-1], map[string]any{"did": "Got access, all good", "s0": StatusInProgress})
	if len(p.roots) != roots || !strings.Contains(p.updates[len(p.updates)-1], "no longer lists blockers") {
		t.Errorf("resolve: new roots %d, last update %q", len(p.roots)-roots, p.updates[len(p.updates)-1])
	}
}

func TestMultiValues(t *testing.T) {
	for _, v := range []any{[]any{"0", "2", ""}, []string{"0", "2"}, "0, 2", "0,2,"} {
		if got := multiValues(v); strings.Join(got, "|") != "0|2" {
			t.Errorf("%#v → %v", v, got)
		}
	}
	if multiValues(nil) != nil || multiValues(42) != nil {
		t.Error("unexpected values")
	}
}

func TestAddJustClosed(t *testing.T) {
	const a, b = "https://github.com/o/r/issues/1", "https://github.com/o/r/issues/2"
	// Search already lists #1; #2 was closed seconds ago and isn't indexed yet.
	act := &Activity{IssuesClosed: 1, ClosedIssues: []string{a}}
	addJustClosed(act, []string{a, b})
	if act.IssuesClosed != 2 || len(act.ClosedIssues) != 2 {
		t.Errorf("got %+v", act)
	}
	// Search listed only part of the closed issues: don't guess.
	act = &Activity{IssuesClosed: 150, ClosedIssues: []string{a}}
	addJustClosed(act, []string{b})
	if act.IssuesClosed != 150 {
		t.Errorf("truncated list changed the count: %+v", act)
	}
	addJustClosed(nil, []string{b}) // activity failed: no panic
}

// A resubmission while a sync runs: the old sync must not mark the new
// version synced; the report is handed back and synced again.
func TestSyncEODResubmitDuringSync(t *testing.T) {
	svc, st, p, ft := newEODService(istAt(2026, 11, 26, 18, 0))
	w := &fakeWriter{}
	svc.Writer = w
	svc.eodQueue = make(chan int64, 4)
	ft.eodOpen = []Task{task(45, StatusInProgress, 9), task(46, StatusInProgress, 8)}
	submitted(t, svc, p, map[string]any{"did": "First version of today", "s0": StatusBlocked, "s1": noChange})
	id := <-svc.eodQueue

	// While #45=Blocked is being written, the member resubmits: #45 Done, #46 Done.
	w.onApply = func() {
		if r := submit(t, svc, p.dialogs[len(p.dialogs)-1], map[string]any{
			"did": "Second version of today", "s0": StatusDone, "s1": StatusDone}); r.Error != "" || len(r.Errors) > 0 {
			t.Fatalf("resubmit: %+v", r)
		}
	}
	svc.SyncEOD(context.Background(), id)

	r := st.reports["u/A/2026-11-26"]
	if r.SyncStatus != "pending" {
		t.Fatalf("old sync finished the new version: %q", r.SyncStatus)
	}
	for _, it := range r.Items {
		if it.Synced {
			t.Errorf("new item marked synced by the old sync: %+v", it)
		}
	}
	// Handed back: queued again (once by the resubmit, once by the old sync).
	if len(svc.eodQueue) == 0 {
		t.Fatal("not queued again")
	}
	for len(svc.eodQueue) > 0 {
		svc.SyncEOD(context.Background(), <-svc.eodQueue)
	}
	r = st.reports["u/A/2026-11-26"]
	if r.SyncStatus != "synced" || strings.Join(w.applied, ",") != "#45=Blocked,#45=Done,#46=Done" {
		t.Errorf("after resync: %q %v", r.SyncStatus, w.applied)
	}
}
