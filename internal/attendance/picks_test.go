package attendance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// orgTask is task() in the fake's org (fakeTasks.OrgName).
func orgTask(n int, status string) Task {
	t := task(n, status, 9)
	t.Repo, t.URL = "inkyank-org/app", fmt.Sprintf("https://github.com/inkyank-org/app/issues/%d", n)
	return t
}

func TestCheckInShowsTasksAndPickMenu(t *testing.T) {
	svc, st, _, ft := newEODService(istAt(2026, 11, 27, 9, 30))
	st.sessions = nil
	ft.tasks = []Task{orgTask(15, StatusTodo), orgTask(5, StatusInProgress)}

	r := svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", Text: "in"})
	if !strings.Contains(r.Text, "Checked in") || !strings.Contains(r.Text, "Your tasks — InkYank (2)") || !strings.Contains(r.Text, "#15") {
		t.Fatalf("reply: %q", r.Text)
	}
	if len(r.Attachments) != 2 || len(r.Attachments[1].Actions) != 1 {
		t.Fatalf("attachments: %+v", r.Attachments)
	}
	menu := r.Attachments[1].Actions[0]
	// Same order as the list (sorted by number here: no priorities or due dates).
	if menu.Type != "select" || len(menu.Options) != 2 || menu.Options[0].Text != "#5 Task 5 (In progress)" ||
		menu.Options[1].Text != "#15 Task 15 (Todo)" || menu.Options[1].Value != orgTask(15, "").URL {
		t.Errorf("menu: %+v", menu)
	}
	fields, err := svc.Signer.(interface {
		Verify(map[string]string) (map[string]string, error)
	}).Verify(menu.Integration.Context)
	if err != nil || fields["a"] != ActPick || fields["u"] != "u" || fields["t"] != "A" {
		t.Errorf("context: %v %v", fields, err)
	}
	if len(st.sessions) != 1 {
		t.Error("not checked in")
	}
}

func TestCheckInWithoutTasks(t *testing.T) {
	// GitHub failing never blocks the check-in.
	svc, st, _, ft := newEODService(istAt(2026, 11, 27, 9, 30))
	st.sessions = nil
	ft.tasksErr = errors.New("github down")
	r := svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", Text: "in"})
	if !strings.Contains(r.Text, "Checked in") || !strings.Contains(r.Text, "Couldn't load your tasks right now — try `/att tasks`") ||
		len(r.Attachments) != 1 || len(st.sessions) != 1 {
		t.Errorf("reply: %q %+v", r.Text, r.Attachments)
	}

	// No GitHub login: check-in exactly as before.
	svc, st, _, _ = newEODService(istAt(2026, 11, 27, 9, 30))
	st.sessions, st.member.GitHubLogin = nil, ""
	r = svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", Text: "in"})
	if strings.Contains(r.Text, "tasks") || len(r.Attachments) != 1 {
		t.Errorf("unlinked member: %q", r.Text)
	}
}

func TestTasksCommandHasPickMenu(t *testing.T) {
	svc, _, _, ft := newEODService(istAt(2026, 11, 27, 14, 0))
	ft.tasks = []Task{orgTask(15, StatusTodo)}
	r := svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", Text: "tasks"})
	if len(r.Attachments) != 1 || r.Attachments[0].Actions[0].Type != "select" {
		t.Errorf("reply: %+v", r)
	}
}

func TestPickClick(t *testing.T) {
	svc, st, _, _ := newEODService(istAt(2026, 11, 27, 0, 30)) // session started on the 26th
	w := &fakeWriter{}
	svc.Writer = w
	pick := func(url string) Reply {
		return svc.HandleButton(context.Background(), Click{
			Action: ActPick, UserID: "u", Fields: map[string]string{"a": ActPick, "u": "u", "t": "A"}, Selected: url,
		})
	}
	u15, u16 := orgTask(15, "").URL, orgTask(16, "").URL

	r := pick(u15)
	if !r.EphemeralOnly || !strings.Contains(r.Text, "▶️ **#15 Task *#15***") || !strings.Contains(r.Text, "is now **In progress**") {
		t.Errorf("pick: %+v", r)
	}
	// Counted for the session's day, the EOD report's day.
	if got := st.picks["u/A/2026-11-26"]; len(got) != 1 || got[0] != u15 {
		t.Errorf("picks: %v", st.picks)
	}

	w.inReview = u16
	if r := pick(u16); !strings.Contains(r.Text, "already **In review**, so it's left as is") {
		t.Errorf("in review: %q", r.Text)
	}
	if len(st.picks["u/A/2026-11-26"]) != 2 {
		t.Errorf("in-review pick not recorded: %v", st.picks)
	}

	// Refusals record nothing.
	w.notMine, w.closedURL, w.failURL = orgTask(17, "").URL, orgTask(18, "").URL, orgTask(19, "").URL
	w.offProject = orgTask(20, "").URL
	for url, want := range map[string]string{
		w.offProject: "isn't one of your tasks",
		w.notMine:    "**#17** isn't assigned to you",
		w.closedURL:  "**#18 Old** is already closed",
		w.failURL:    "Couldn't update GitHub",
	} {
		if r := pick(url); !strings.Contains(r.Text, want) {
			t.Errorf("%s: %q", url, r.Text)
		}
	}
	if len(st.picks["u/A/2026-11-26"]) != 2 {
		t.Errorf("refused picks recorded: %v", st.picks)
	}

	// The chosen value isn't signed: anything but an issue in the team's
	// org is refused before GitHub is called.
	calls := len(w.started)
	for _, bad := range []string{"", "https://github.com/other-org/app/issues/1", "https://evil.example/inkyank-org/app/issues/1",
		"https://github.com/inkyank-org/app/pull/3", "https://github.com/inkyank-org/app/issues/3?x=1"} {
		if r := pick(bad); !strings.Contains(r.Text, "isn't one of your tasks") {
			t.Errorf("%q: %q", bad, r.Text)
		}
	}
	if len(w.started) != calls {
		t.Error("GitHub called for a bad value")
	}
}

func TestPickedTaskGetsEODDropdown(t *testing.T) {
	svc, st, p, ft := newEODService(istAt(2026, 11, 26, 18, 0))
	todo := orgTask(46, StatusTodo)
	ft.eodOpen = []Task{orgTask(45, StatusInProgress), todo, orgTask(47, StatusTodo)}
	_ = st.AddTaskPick(context.Background(), "u", "A", "2026-11-26", todo.URL)

	svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", TriggerID: "trig", Text: "out"})
	if len(p.dialogs) != 1 {
		t.Fatal("form not opened")
	}
	var selects []string
	var others []string
	for _, e := range p.dialogs[0].Elements {
		switch {
		case e.Name == "others":
			for _, o := range e.Options {
				others = append(others, o.Text)
			}
		case e.Type == "select":
			selects = append(selects, e.DisplayName)
		}
	}
	// Picked #46 gets a dropdown even though it's Todo; unpicked #47 doesn't.
	if strings.Join(selects, ",") != "#45 Task 45,#46 Task 46" || strings.Join(others, ",") != "#47 Task 47" {
		t.Errorf("selects %v, others %v", selects, others)
	}
}
