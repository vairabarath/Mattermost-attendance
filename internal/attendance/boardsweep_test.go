package attendance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeSweeper struct {
	calls []string // team@since(full|HH:MM)
	fail  map[string]error
	added []SweptIssue
}

func (f *fakeSweeper) Configured(team string) bool { return team == "A" || team == "B" }
func (f *fakeSweeper) SweepBoard(_ context.Context, team string, since time.Time, linked map[string]bool) ([]SweptIssue, error) {
	s := "full"
	if !since.IsZero() {
		s = since.In(ist).Format("15:04")
	}
	var logins []string
	for l := range linked {
		logins = append(logins, l)
	}
	f.calls = append(f.calls, team+"@"+s+"/"+strings.Join(logins, ","))
	if err := f.fail[team]; err != nil {
		return f.added, err
	}
	return f.added, nil
}

func TestSweepBoards(t *testing.T) {
	st := newFakeJobStore()
	st.members["u1"] = MemberSettings{UserID: "u1", GitHubLogin: "Jana-Dev"}
	st.members["u2"] = MemberSettings{UserID: "u2"} // not linked
	j, _ := newJobs(st, nil)
	sw := &fakeSweeper{fail: map[string]error{}}
	j.Board = sw
	now := istAt(2026, 11, 27, 10, 0)
	j.Now = func() time.Time { return now }
	ctx := context.Background()

	// First run: everything (no watermark yet), with linked logins lowercased.
	j.SweepBoards(ctx, false)
	if strings.Join(sw.calls, " ") != "A@full/jana-dev B@full/jana-dev" {
		t.Fatalf("first run: %v", sw.calls)
	}
	// Next run: only issues updated since the last run, minus the overlap.
	sw.calls = nil
	sw.fail["B"] = errors.New("github down")
	now = istAt(2026, 11, 27, 10, 2)
	j.SweepBoards(ctx, false)
	if strings.Join(sw.calls, " ") != "A@09:59/jana-dev B@09:59/jana-dev" {
		t.Fatalf("incremental: %v", sw.calls)
	}
	// B failed: it keeps its old watermark and covers the gap next time.
	sw.calls, sw.fail["B"] = nil, nil
	now = istAt(2026, 11, 27, 10, 4)
	j.SweepBoards(ctx, false)
	if strings.Join(sw.calls, " ") != "A@10:01/jana-dev B@09:59/jana-dev" {
		t.Fatalf("after a failure: %v", sw.calls)
	}
	// The daily full run ignores the watermark.
	sw.calls = nil
	j.SweepBoards(ctx, true)
	if strings.Join(sw.calls, " ") != "A@full/jana-dev B@full/jana-dev" {
		t.Fatalf("full: %v", sw.calls)
	}
	// A run still going makes the next one skip.
	sw.calls = nil
	j.sweepMu.Lock()
	j.SweepBoards(ctx, false)
	j.sweepMu.Unlock()
	if len(sw.calls) != 0 {
		t.Errorf("overlapping run: %v", sw.calls)
	}
	// Nobody linked: nothing to look for.
	delete(st.members, "u1")
	j.SweepBoards(ctx, false)
	if len(sw.calls) != 0 {
		t.Errorf("no linked members: %v", sw.calls)
	}
}

// Off-board issues assigned to the member are listed right away (🆕) and
// added to the board in the background.
func TestOffBoardTasksListedAndAdded(t *testing.T) {
	svc, _, _, ft := newEODService(istAt(2026, 11, 27, 9, 30))
	var bg []func()
	svc.Background = func(f func()) { bg = append(bg, f) }
	off := orgTask(30, StatusTodo)
	off.OffBoard = true
	ft.tasks = []Task{orgTask(15, StatusInProgress), off}

	r := svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", Text: "tasks"})
	if !strings.Contains(r.Text, "Task 30 — Todo 🆕 added to the board") || strings.Contains(r.Text, "Task 15 — In progress 🆕") {
		t.Errorf("list: %q", r.Text)
	}
	if len(r.Attachments) != 1 || len(r.Attachments[0].Actions[0].Options) != 2 {
		t.Errorf("off-board task not pickable: %+v", r.Attachments)
	}
	if len(ft.addedToBoard) != 0 || len(bg) != 1 {
		t.Fatalf("must run in the background: added=%v bg=%d", ft.addedToBoard, len(bg))
	}
	bg[0]()
	if strings.Join(ft.addedToBoard, ",") != "#30" || ft.boardTeam != "A" {
		t.Errorf("added: %v to %q", ft.addedToBoard, ft.boardTeam)
	}

	// Nothing off the board: no background work.
	bg, ft.tasks = nil, []Task{orgTask(15, StatusInProgress)}
	svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", Text: "tasks"})
	if len(bg) != 0 {
		t.Error("background work without off-board issues")
	}
}

// Assignees' panels refresh: for added issues (also when the run failed
// after adding), and on incremental runs for issues seen as changed. Full
// runs only notify for what they added.
func TestSweepNotifiesAssignees(t *testing.T) {
	st := newFakeJobStore()
	st.members["u1"] = MemberSettings{UserID: "u1", GitHubLogin: "Jana-Dev"}
	st.members["u2"] = MemberSettings{UserID: "u2", GitHubLogin: "vairabarath"}
	st.members["u3"] = MemberSettings{UserID: "u3", GitHubLogin: "ravi-gh"}
	j, _ := newJobs(st, nil)
	var got notes
	j.Notify = got.record
	sw := &fakeSweeper{fail: map[string]error{"B": errors.New("502")}, added: []SweptIssue{
		{Assignees: []string{"jana-dev"}, Added: true},
		{Assignees: []string{"jana-dev", "vairabarath"}, Added: true},
		{Assignees: []string{"ravi-gh"}}, // on the board already (e.g. GitHub's Auto-add), just changed
	}}
	j.Board = sw
	j.Now = func() time.Time { return istAt(2026, 11, 27, 10, 0) }

	j.SweepBoards(context.Background(), true) // full run: only what it added
	if strings.Join(got, ",") != "u1:tasks_changed:A,u2:tasks_changed:A,u1:tasks_changed:B,u2:tasks_changed:B" {
		t.Errorf("full run notified %v", got)
	}
	got = nil
	j.SweepBoards(context.Background(), false) // incremental (A has a watermark now)
	if strings.Join(got, ",") != "u1:tasks_changed:A,u2:tasks_changed:A,u3:tasks_changed:A,u1:tasks_changed:B,u2:tasks_changed:B" {
		t.Errorf("incremental run notified %v", got)
	}
}
