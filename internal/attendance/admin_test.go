package attendance

import (
	"context"
	"testing"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

type fakeRoles struct {
	users   map[string]*mattermost.User
	members map[string]*mattermost.TeamMember // key team/user
	calls   int
}

func (f *fakeRoles) GetUser(_ context.Context, id string) (*mattermost.User, error) {
	f.calls++
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return &mattermost.User{ID: id, Roles: "system_user"}, nil
}

func (f *fakeRoles) GetTeamMember(_ context.Context, team, user string) (*mattermost.TeamMember, error) {
	return f.members[team+"/"+user], nil
}

func roles() *fakeRoles {
	return &fakeRoles{
		users: map[string]*mattermost.User{
			"sys": {ID: "sys", Roles: "system_user system_admin"},
		},
		members: map[string]*mattermost.TeamMember{
			"A/teamadmin": {SchemeAdmin: true, Roles: "team_user"},
			"B/teamadmin": {Roles: "team_user"},
			"A/legacy":    {Roles: "team_user team_admin"},
			"A/plain":     {Roles: "team_user"},
		},
	}
}

func TestAdminCheck(t *testing.T) {
	a := NewAdminChecker(roles(), time.Minute)
	cases := []struct {
		team, user string
		want       bool
	}{
		{"A", "sys", true}, // system admin: any team
		{"B", "sys", true},
		{"A", "teamadmin", true},  // scheme_admin
		{"B", "teamadmin", false}, // team admin of A is not admin of B
		{"A", "legacy", true},     // team_admin role
		{"A", "plain", false},
		{"A", "stranger", false}, // not a member at all
	}
	for _, c := range cases {
		got, err := a.IsAdmin(context.Background(), c.team, c.user)
		if err != nil || got != c.want {
			t.Errorf("IsAdmin(%s,%s) = %v,%v want %v", c.team, c.user, got, err, c.want)
		}
	}
	if sys, _ := a.IsSystemAdmin(context.Background(), "A", "teamadmin"); sys {
		t.Error("team admin reported as system admin")
	}
}

func TestAdminCheckCached(t *testing.T) {
	f := roles()
	a := NewAdminChecker(f, time.Minute)
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		_, _ = a.IsAdmin(context.Background(), "A", "plain")
	}
	if f.calls != 1 {
		t.Fatalf("expected 1 API call, got %d", f.calls)
	}
	now = now.Add(2 * time.Minute)
	_, _ = a.IsAdmin(context.Background(), "A", "plain")
	if f.calls != 2 {
		t.Fatalf("cache not expired: %d calls", f.calls)
	}
}
