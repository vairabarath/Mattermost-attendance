package attendance

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

func TestSortTasks(t *testing.T) {
	today := "2026-11-26"
	tasks := []Task{
		{Number: 1, Priority: "Low"},
		{Number: 2, Priority: "High", Due: "2026-12-05"},
		{Number: 3, Priority: "Low", Due: "2026-11-20"}, // overdue
		{Number: 4}, // no priority
		{Number: 5, Priority: "High", Due: "2026-12-01"},
		{Number: 6, Priority: "Medium", Due: "2026-11-25"}, // overdue
		{Number: 7, Priority: "High"},
		{Number: 8, Priority: "Medium", Due: "2026-11-26"}, // due today: not overdue
	}
	SortTasks(tasks, today)
	var got []int
	for _, task := range tasks {
		got = append(got, task.Number)
	}
	want := []int{6, 3, 5, 2, 7, 8, 1, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}

func TestRenderTaskList(t *testing.T) {
	today := "2026-11-26"
	tasks := []Task{
		{Repo: "o/app", Number: 42, Title: "Fix [login] | crash", URL: "https://x/42", Status: StatusInProgress, Priority: "High", Due: "2026-11-24"},
		{Repo: "o/app", Number: 47, Title: "Export CSV", URL: "https://x/47", Status: StatusBlocked},
	}
	out := RenderTaskList(tasks, today, ist)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("lines: %q", out)
	}
	if lines[0] != `- ⚠️ [#42](https://x/42) Fix \[login\] \| crash — In progress — High — due Tue 24 Nov` {
		t.Errorf("line 1: %q", lines[0])
	}
	if lines[1] != "- 🚧 [#47](https://x/47) Export CSV — Blocked" {
		t.Errorf("line 2: %q", lines[1])
	}

	// Several repos: the repo name disambiguates the number.
	tasks = append(tasks, Task{Repo: "o/operations", Number: 7, Title: "Renew domain", URL: "https://x/7"})
	if out := RenderTaskList(tasks, today, ist); !strings.Contains(out, "[operations#7]") || !strings.Contains(out, "No status") {
		t.Errorf("multi repo: %q", out)
	}

	var many []Task
	for i := 1; i <= 13; i++ {
		many = append(many, Task{Repo: "o/app", Number: i, URL: "u"})
	}
	out = RenderTaskList(many, today, ist)
	if n := strings.Count(out, "\n- "); n != maxTaskLines-1 || !strings.HasSuffix(out, "_…and 3 more._") {
		t.Errorf("limit: %d lines\n%s", n+1, out)
	}
}

func TestValidGitHubLogin(t *testing.T) {
	for _, ok := range []string{"jana", "Jana-Dev", "a", "a1-b2-c3", strings.Repeat("x", 39)} {
		if !ValidGitHubLogin(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "-jana", "jana-", "ja--na", "ja_na", "jana dev", strings.Repeat("x", 40), "x/../y"} {
		if ValidGitHubLogin(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// --- service tests ---

type fakeTasks struct {
	eodOpen, eodClosed []Task
	eodErr             error
	configured         map[string]bool
	verify             map[string]error // lower(login) → error
	tasks              []Task
	tasksErr           error
	gotLogin           string
	delay              time.Duration
}

func (f *fakeTasks) Configured(teamID string) bool { return f.configured[teamID] }
func (f *fakeTasks) OrgName(string) string         { return "inkyank-org" }
func (f *fakeTasks) VerifyLogin(_ context.Context, _, login string) (string, error) {
	if err, ok := f.verify[strings.ToLower(login)]; ok && err != nil {
		return "", err
	}
	if strings.EqualFold(login, "jana-dev") {
		return "Jana-Dev", nil
	}
	return login, nil
}
func (f *fakeTasks) EODTasks(_ context.Context, _, login string, _ time.Time) ([]Task, []Task, error) {
	f.gotLogin = login
	return f.eodOpen, f.eodClosed, f.eodErr
}
func (f *fakeTasks) AssignedTasks(ctx context.Context, _, login string) ([]Task, error) {
	f.gotLogin = login
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.tasks, f.tasksErr
}

type ghStore struct {
	Store
	members map[string]*MemberSettings
	audits  []AuditEntry
}

func (f *ghStore) MemberRefreshedAt(context.Context, string) (time.Time, bool, error) {
	return time.Now(), true, nil
}
func (f *ghStore) UpsertMember(context.Context, string, string, string) error { return nil }
func (f *ghStore) GetMember(_ context.Context, id string) (*MemberSettings, error) {
	m, ok := f.members[id]
	if !ok {
		return nil, nil
	}
	c := *m
	return &c, nil
}
func (f *ghStore) UpdateMember(_ context.Context, id string, upd MemberUpdate) (MemberSettings, MemberSettings, error) {
	m := f.members[id]
	before := *m
	if upd.GitHubLogin != nil {
		for other, om := range f.members {
			if other != id && *upd.GitHubLogin != "" && strings.EqualFold(om.GitHubLogin, *upd.GitHubLogin) {
				return before, before, ErrGitHubLoginTaken
			}
		}
		m.GitHubLogin = *upd.GitHubLogin
	}
	return before, *m, nil
}
func (f *ghStore) InsertAudit(_ context.Context, e AuditEntry) error {
	f.audits = append(f.audits, e)
	return nil
}

type fakePeople struct{ *fakeRoles }

func (p fakePeople) GetUserByUsername(_ context.Context, name string) (*mattermost.User, error) {
	ids := map[string]string{"jana": "jana", "plain": "plain"}
	if id, ok := ids[name]; ok {
		return &mattermost.User{ID: id, Username: name}, nil
	}
	return nil, nil
}

func newTaskService() (*Service, *ghStore, *fakeTasks) {
	st := &ghStore{members: map[string]*MemberSettings{
		"jana":      {UserID: "jana", Username: "jana"},
		"plain":     {UserID: "plain", Username: "plain", GitHubLogin: "taken-login"},
		"teamadmin": {UserID: "teamadmin", Username: "barath"},
		"sys":       {UserID: "sys", Username: "root"},
	}}
	ft := &fakeTasks{configured: map[string]bool{"A": true}, verify: map[string]error{}}
	r := roles()
	r.members["A/jana"] = &mattermost.TeamMember{Roles: "team_user"}
	svc := &Service{
		Store: st, Teams: teamNames{"A": "InkYank", "B": "Seechanlabs"}, Tasks: ft,
		Admins: NewAdminChecker(r, time.Minute), People: fakePeople{r},
		Loc: ist, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return istAt(2026, 11, 26, 10, 0) },
	}
	return svc, st, ft
}

func run(svc *Service, team, user, cmd string) string {
	return svc.Handle(context.Background(), Command{TeamID: team, UserID: user, Text: cmd}).Text
}

func TestGitHubLinkCommand(t *testing.T) {
	svc, st, ft := newTaskService()

	if out := run(svc, "A", "jana", "github"); !strings.Contains(out, "isn't linked yet") || !strings.Contains(out, "/att member @you github") {
		t.Errorf("show unlinked: %q", out)
	}
	// Members can't link themselves: they could claim anyone's login.
	for _, cmd := range []string{"github jana-dev", "github @someone-else"} {
		if out := run(svc, "A", "jana", cmd); !strings.Contains(out, "🔒 Only admins can link GitHub accounts") {
			t.Errorf("member %q: %q", cmd, out)
		}
	}
	if st.members["jana"].GitHubLogin != "" || len(st.audits) != 0 {
		t.Fatalf("member self-link changed something: %q %+v", st.members["jana"].GitHubLogin, st.audits)
	}
	// A team admin of A is a member in B: refused there too.
	if out := run(svc, "B", "teamadmin", "github barath-gh"); !strings.Contains(out, "🔒") {
		t.Errorf("admin of another team: %q", out)
	}

	// Admins may link themselves.
	if out := run(svc, "A", "teamadmin", "github @barath-gh"); !strings.Contains(out, "Linked to GitHub **barath-gh**") {
		t.Fatalf("admin self-link: %q", out)
	}
	if st.members["teamadmin"].GitHubLogin != "barath-gh" || len(st.audits) != 1 || st.audits[0].Action != "github_link" {
		t.Errorf("stored %q, audits %+v", st.members["teamadmin"].GitHubLogin, st.audits)
	}
	if out := run(svc, "A", "teamadmin", "github"); !strings.Contains(out, "barath-gh") || !strings.Contains(out, "ask an admin") {
		t.Errorf("show linked: %q", out)
	}

	cases := map[string]string{
		"github taken-login": "already linked to another member",
		"github not_valid!":  "isn't a valid GitHub username",
		"github a b":         "Usage",
		"github ghost":       "There's no GitHub user",
		"github outsider":    "isn't a member of the **inkyank-org** GitHub organization",
		"github flaky":       "Couldn't reach GitHub",
	}
	ft.verify["ghost"] = ErrGitHubUserNotFound
	ft.verify["outsider"] = ErrNotOrgMember
	ft.verify["flaky"] = errors.New("timeout")
	for cmd, want := range cases {
		if out := run(svc, "A", "teamadmin", cmd); !strings.Contains(out, want) {
			t.Errorf("%s: %q, want %q", cmd, out, want)
		}
	}
	if st.members["teamadmin"].GitHubLogin != "barath-gh" {
		t.Error("a failed link changed the stored login")
	}
	if out := run(svc, "B", "sys", "github someone"); !strings.Contains(out, "isn't set up for **Seechanlabs**") {
		t.Errorf("team without GitHub: %q", out)
	}
}

func TestMemberGitHubCommand(t *testing.T) {
	svc, st, _ := newTaskService()
	if out := run(svc, "A", "jana", "member @plain github x"); !strings.Contains(out, "🔒") {
		t.Errorf("non-admin: %q", out)
	}
	if out := run(svc, "A", "teamadmin", "member @jana github jana-dev"); !strings.Contains(out, "GitHub login Jana-Dev") {
		t.Fatalf("admin set: %q", out)
	}
	if st.members["jana"].GitHubLogin != "Jana-Dev" || st.audits[0].Action != "member_settings" || st.audits[0].ActorID != "teamadmin" {
		t.Errorf("stored %q audits %+v", st.members["jana"].GitHubLogin, st.audits)
	}
	if out := run(svc, "A", "teamadmin", "member @jana"); !strings.Contains(out, "| GitHub | Jana-Dev |") {
		t.Errorf("settings view: %q", out)
	}
	if out := run(svc, "A", "teamadmin", "member @jana github taken-login"); !strings.Contains(out, "already linked") {
		t.Errorf("taken: %q", out)
	}
	if out := run(svc, "A", "teamadmin", "member @jana github none"); !strings.Contains(out, "unlinked") || st.members["jana"].GitHubLogin != "" {
		t.Errorf("unlink: %q %q", out, st.members["jana"].GitHubLogin)
	}
}

func TestTasksCommand(t *testing.T) {
	svc, st, ft := newTaskService()
	if out := run(svc, "A", "jana", "tasks"); !strings.Contains(out, "/att member @you github") {
		t.Errorf("unlinked: %q", out)
	}
	st.members["jana"].GitHubLogin = "Jana-Dev"
	if out := run(svc, "A", "jana", "tasks"); !strings.Contains(out, "No open tasks") {
		t.Errorf("none: %q", out)
	}
	ft.tasks = []Task{
		{Repo: "o/app", Number: 45, Title: "Later", URL: "u45", Status: StatusTodo, Priority: "Low"},
		{Repo: "o/app", Number: 42, Title: "Urgent", URL: "u42", Status: StatusInProgress, Priority: "High", Due: "2026-11-20"},
	}
	out := run(svc, "A", "jana", "tasks")
	if ft.gotLogin != "Jana-Dev" || !strings.Contains(out, "Your open tasks — InkYank (2)") ||
		strings.Index(out, "#42") > strings.Index(out, "#45") || !strings.Contains(out, "⚠️ [#42]") {
		t.Errorf("list: %q", out)
	}

	ft.tasksErr = errors.New("boom")
	if out := run(svc, "A", "jana", "tasks"); !strings.Contains(out, "Couldn't load your tasks") {
		t.Errorf("error: %q", out)
	}
	ft.tasksErr = ErrGitHubNotSetUp
	if out := run(svc, "A", "jana", "tasks"); !strings.Contains(out, "isn't set up as the bot needs") {
		t.Errorf("misconfigured: %q", out)
	}
	if out := run(svc, "B", "jana", "tasks"); !strings.Contains(out, "isn't set up for **Seechanlabs**") {
		t.Errorf("no github: %q", out)
	}
}

func TestHelpListsGitHubCommands(t *testing.T) {
	if h := helpText(false); !strings.Contains(h, "/att tasks") || !strings.Contains(h, "/att github") || strings.Contains(h, "member @user github") {
		t.Errorf("member help: %s", h)
	}
	if h := helpText(true); !strings.Contains(h, "member @user github") {
		t.Errorf("admin help: %s", h)
	}
}
