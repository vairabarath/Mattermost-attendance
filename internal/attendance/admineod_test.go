package attendance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

func (f *eodStore) AllSessionsOverlapping(ctx context.Context, from, to time.Time) (map[string][]Session, error) {
	ss, _ := f.SessionsOverlapping(ctx, "u", from, to)
	out := map[string][]Session{"u": ss}
	for uid, extra := range f.others {
		for _, s := range extra {
			if s.CheckIn.Before(to) && (s.CheckOut == nil || s.CheckOut.After(from)) {
				out[uid] = append(out[uid], s)
			}
		}
	}
	return out, nil
}
func (f *eodStore) AllMembers(context.Context) (map[string]MemberSettings, error) {
	return map[string]MemberSettings{"u": f.member, "u2": {UserID: "u2", Username: "barath"}, "u3": {UserID: "u3", Username: "ravi"}}, nil
}
func (f *eodStore) EODStatuses(_ context.Context, team, from, to string) ([]EODStatus, error) {
	var out []EODStatus
	for _, r := range f.reports {
		if r.TeamID == team && r.Day >= from && r.Day <= to {
			st := EODStatus{UserID: r.UserID, TeamID: r.TeamID, Day: r.Day, SyncStatus: r.SyncStatus}
			for _, it := range r.Items {
				switch it.NewStatus {
				case StatusDone:
					st.Done++
				case StatusBlocked:
					st.Blocked++
				}
			}
			out = append(out, st)
		}
	}
	return out, nil
}
func (f *eodStore) EODThreadRoot(_ context.Context, team, day string) (string, error) {
	return f.threads[team+day], nil
}

// eodPeople resolves @jana to the eodStore's member "u".
type eodPeople struct{ *fakeRoles }

func (p eodPeople) GetUserByUsername(_ context.Context, name string) (*mattermost.User, error) {
	switch name {
	case "jana":
		return &mattermost.User{ID: "u", Username: "jana"}, nil
	case "outsider":
		return &mattermost.User{ID: "x", Username: "outsider"}, nil
	}
	return nil, nil
}

func newAdminEODService(now time.Time) (*Service, *eodStore, *eodPoster, *fakeTasks) {
	svc, st, p, ft := newEODService(now)
	r := roles()
	r.members["A/u"] = &mattermost.TeamMember{Roles: "team_user"}
	svc.Admins, svc.People = NewAdminChecker(r, time.Minute), eodPeople{r}
	return svc, st, p, ft
}

func TestAdminEODViews(t *testing.T) {
	svc, st, p, ft := newAdminEODService(istAt(2026, 11, 26, 18, 0))
	ft.eodOpen = []Task{task(45, StatusInProgress, 9), task(47, StatusInProgress, 8)}
	submitted(t, svc, p, map[string]any{"did": "Finished the timeout work", "s0": StatusDone, "s1": StatusBlocked})
	o := istAt(2026, 11, 26, 16, 0)
	st.others = map[string][]Session{
		"u2": {{TeamID: "A", CheckIn: istAt(2026, 11, 26, 9, 0), CheckOut: &o}},
		"u3": {{TeamID: "A", CheckIn: istAt(2026, 11, 26, 11, 0)}},              // still in
		"u4": {{TeamID: "B", CheckIn: istAt(2026, 11, 26, 9, 0), CheckOut: &o}}, // other team
	}

	// Members can't use them.
	for _, cmd := range []string{"eod team", "eod @jana", "tasks @jana"} {
		if out := run(svc, "A", "u", cmd); !strings.Contains(out, "Only admins") {
			t.Errorf("%s as member: %q", cmd, out)
		}
	}

	team := run(svc, "A", "teamadmin", "eod team")
	for _, want := range []string{
		"#### 📝 End-of-day reports — InkYank — Thu 26 Nov", "**Sent: 1 of 3**",
		"| @barath | 7h 0m | ❌ |", "| @jana | 8h 0m | ✅ | 1 | 1 🚧 |", "| @ravi | 7h 0m | ⏳ |",
		"[All reports in the thread]",
	} {
		if !strings.Contains(team, want) {
			t.Errorf("team: missing %q in\n%s", want, team)
		}
	}
	if strings.Contains(team, "unknown user") {
		t.Errorf("other team's member listed:\n%s", team)
	}

	one := run(svc, "A", "teamadmin", "eod @jana 2026-11-26")
	if !strings.Contains(one, "#### @jana's end-of-day report — InkYank, Thu 26 Nov") || !strings.Contains(one, "Finished the timeout work") ||
		!strings.Contains(one, "[open in thread]") {
		t.Errorf("member view:\n%s", one)
	}
	if out := run(svc, "A", "teamadmin", "eod @jana 2026-11-25"); !strings.Contains(out, "didn't work in **InkYank** on Wed 25 Nov") {
		t.Errorf("other day: %q", out)
	}
	if out := run(svc, "A", "teamadmin", "eod @outsider"); !strings.Contains(out, "isn't a member of **InkYank**") {
		t.Errorf("outsider: %q", out)
	}
	if out := run(svc, "A", "teamadmin", "eod team 26-11-2026"); !strings.Contains(out, "/att eod team [YYYY-MM-DD]") {
		t.Errorf("bad date: %q", out)
	}
}

func TestAdminEODMissingReport(t *testing.T) {
	svc, _, _, _ := newAdminEODService(istAt(2026, 11, 26, 18, 0))
	if out := run(svc, "A", "teamadmin", "eod @jana"); !strings.Contains(out, "❌ @jana worked in **InkYank** on Thu 26 Nov but hasn't sent") {
		t.Errorf("got %q", out)
	}
}

func TestAdminMemberTasks(t *testing.T) {
	svc, st, _, ft := newAdminEODService(istAt(2026, 11, 26, 10, 0))
	ft.tasks = []Task{orgTask(15, StatusTodo)}
	out := run(svc, "A", "teamadmin", "tasks @jana")
	if !strings.Contains(out, "@jana's open tasks — InkYank (1)") || ft.gotLogin != "Jana-Dev" {
		t.Errorf("got %q (login %q)", out, ft.gotLogin)
	}
	st.member.GitHubLogin = ""
	if out := run(svc, "A", "teamadmin", "tasks @jana"); !strings.Contains(out, "no linked GitHub login") {
		t.Errorf("unlinked: %q", out)
	}
	if out := run(svc, "A", "teamadmin", "tasks jana"); !strings.Contains(out, "Usage") {
		t.Errorf("no @: %q", out)
	}
}
