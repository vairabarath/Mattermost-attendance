package attendance

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPanelTasks(t *testing.T) {
	svc, st, _, ft := newAdminEODService(istAt(2026, 11, 27, 10, 0))
	var bg []func()
	svc.Background = func(f func()) { bg = append(bg, f) }
	ctx := context.Background()

	over := orgTask(9, StatusTodo)
	over.Due, over.Priority = "2026-11-25", "High"
	off := orgTask(30, StatusTodo)
	off.OffBoard = true
	ft.tasks = []Task{orgTask(15, StatusInProgress), over, off}
	_ = st.AddTaskPick(ctx, "u", "A", "2026-11-26", orgTask(15, "").URL) // the open session started on the 26th

	got, err := svc.PanelTasks(ctx, "u", "A")
	if err != nil || !got.Configured || !got.Linked || got.Error != "" || len(got.Tasks) != 3 {
		t.Fatalf("tasks: %+v %v", got, err)
	}
	// Sorted like /att tasks: overdue first.
	first := got.Tasks[0]
	if first.Number != 9 || !first.Overdue || first.Priority != "High" || first.Due != "2026-11-25" {
		t.Errorf("first: %+v", first)
	}
	byNum := map[int]TaskItem{}
	for _, it := range got.Tasks {
		byNum[it.Number] = it
	}
	if !byNum[15].Picked || byNum[9].Picked || !byNum[30].OffBoard || byNum[15].Status != StatusInProgress {
		t.Errorf("flags: %+v", byNum)
	}
	if len(bg) != 1 {
		t.Error("off-board task not added in the background")
	}

	// Not linked, GitHub failing, GitHub not set up.
	st.member.GitHubLogin = ""
	if got, _ := svc.PanelTasks(ctx, "u", "A"); !got.Configured || got.Linked || len(got.Tasks) != 0 {
		t.Errorf("unlinked: %+v", got)
	}
	st.member.GitHubLogin = "Jana-Dev"
	ft.tasksErr = errors.New("502")
	if got, _ := svc.PanelTasks(ctx, "u", "A"); got.Error == "" || got.Tasks == nil {
		t.Errorf("github down: %+v", got)
	}
	ft.configured = map[string]bool{}
	if got, _ := svc.PanelTasks(ctx, "u", "A"); got.Configured || got.Linked {
		t.Errorf("not set up: %+v", got)
	}
	if _, err := svc.PanelTasks(ctx, "x", "A"); !errors.Is(err, ErrNotTeamMember) {
		t.Errorf("non-member: %v", err)
	}
}

func TestPanelPick(t *testing.T) {
	svc, st, _, _ := newAdminEODService(istAt(2026, 11, 27, 10, 0))
	w := &fakeWriter{}
	svc.Writer = w
	var got notes
	svc.Notify = got.record
	ctx := context.Background()
	url := orgTask(15, "").URL

	msg, err := svc.PanelPick(ctx, "u", "A", url)
	if err != nil || !strings.Contains(msg, "is now **In progress**") {
		t.Fatalf("pick: %q %v", msg, err)
	}
	if len(st.picks["u/A/2026-11-26"]) != 1 || strings.Join(got, ",") != "u:tasks_changed:A" {
		t.Errorf("recorded %v, notified %v", st.picks, got)
	}
	// Same checks as the chat menu: another org's issue is refused before GitHub.
	if msg, _ := svc.PanelPick(ctx, "u", "A", "https://github.com/evil/app/issues/1"); !strings.Contains(msg, "isn't one of your tasks") {
		t.Errorf("other org: %q", msg)
	}
	if _, err := svc.PanelPick(ctx, "x", "A", url); !errors.Is(err, ErrNotTeamMember) {
		t.Errorf("non-member: %v", err)
	}
}
