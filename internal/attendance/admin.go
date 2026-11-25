package attendance

import (
	"context"
	"sync"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// RoleAPI is what the admin check needs from Mattermost.
type RoleAPI interface {
	GetUser(ctx context.Context, userID string) (*mattermost.User, error)
	// GetTeamMember returns nil (no error) if the user isn't in the team.
	GetTeamMember(ctx context.Context, teamID, userID string) (*mattermost.TeamMember, error)
}

// AdminChecker decides who is an admin for a team (spec section 4):
// a system admin (any team), or a team admin of that team via scheme_admin
// or the team_admin role. Results are cached briefly.
type AdminChecker struct {
	api RoleAPI
	ttl time.Duration
	now func() time.Time

	mu    sync.Mutex
	cache map[string]adminEntry // key: teamID + "/" + userID
}

type adminEntry struct {
	system, team bool
	at           time.Time
}

// NewAdminChecker caches each answer for ttl (spec: ~60s).
func NewAdminChecker(api RoleAPI, ttl time.Duration) *AdminChecker {
	return &AdminChecker{api: api, ttl: ttl, now: time.Now, cache: map[string]adminEntry{}}
}

// IsAdmin reports whether userID may run admin commands in teamID.
func (a *AdminChecker) IsAdmin(ctx context.Context, teamID, userID string) (bool, error) {
	e, err := a.lookup(ctx, teamID, userID)
	return e.system || e.team, err
}

// IsSystemAdmin reports whether userID is a system admin.
func (a *AdminChecker) IsSystemAdmin(ctx context.Context, teamID, userID string) (bool, error) {
	e, err := a.lookup(ctx, teamID, userID)
	return e.system, err
}

func (a *AdminChecker) lookup(ctx context.Context, teamID, userID string) (adminEntry, error) {
	key := teamID + "/" + userID
	a.mu.Lock()
	e, ok := a.cache[key]
	a.mu.Unlock()
	if ok && a.now().Sub(e.at) < a.ttl {
		return e, nil
	}

	u, err := a.api.GetUser(ctx, userID)
	if err != nil {
		return adminEntry{}, err
	}
	e = adminEntry{system: u.IsSystemAdmin(), at: a.now()}
	if !e.system {
		// system_admin never appears on the team membership, so the team
		// check is only needed for everyone else.
		m, err := a.api.GetTeamMember(ctx, teamID, userID)
		if err != nil {
			return adminEntry{}, err
		}
		e.team = m.IsTeamAdmin()
	}

	a.mu.Lock()
	a.cache[key] = e
	a.mu.Unlock()
	return e, nil
}
