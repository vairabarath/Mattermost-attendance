package attendance

import (
	"context"
	"errors"
	"time"
)

// The panel API (ui-plan.md): the Mattermost plugin calls these for the
// logged-in user, through internal/handlers. Actions run the same functions
// as the /att commands, so both always behave the same.

// ErrNotTeamMember refuses API calls for a team the user isn't in.
var ErrNotTeamMember = errors.New("not a member of this team")

// Live-update events the plugin pushes to the user's panel.
const (
	EventSession = "session_changed"
)

// TeamRef names a team.
type TeamRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SessionState is the user's open session (in any team).
type SessionState struct {
	Team       TeamRef    `json:"team"`
	CheckIn    time.Time  `json:"check_in"`
	Mode       string     `json:"mode"` // office, wfh
	OnBreak    bool       `json:"on_break"`
	BreakSince *time.Time `json:"break_since,omitempty"`
	// NetMinutes / NetSeconds: this session's work so far (breaks
	// excluded). The panel ticks the seconds on from here.
	NetMinutes int `json:"net_minutes"`
	NetSeconds int `json:"net_seconds"`
}

// MeState is everything the panel's status card needs.
type MeState struct {
	Now          time.Time     `json:"now"`
	UserID       string        `json:"user_id"`
	Team         TeamRef       `json:"team"`
	IsAdmin      bool          `json:"is_admin"`
	GitHubLinked bool          `json:"github_linked"`
	Session      *SessionState `json:"session"` // nil: not checked in anywhere
	// Today's net work: in this team and in all organizations.
	TodayTeamMinutes int `json:"today_team_minutes"`
	TodayAllMinutes  int `json:"today_all_minutes"`
	TodayTeamSeconds int `json:"today_team_seconds"`
	TodayAllSeconds  int `json:"today_all_seconds"`
}

// Session actions for Act.
const (
	ActionIn    = "in"
	ActionBreak = "break"
	ActionBack  = "back"
	ActionOut   = "out"
)

// checkMember registers the user (as the slash commands do) and refuses a
// team they're not a member of.
func (s *Service) checkMember(ctx context.Context, userID, teamID string) error {
	if _, ok := s.TeamCfg[teamID]; !ok {
		return ErrNotTeamMember
	}
	if s.People == nil {
		return ErrNotTeamMember
	}
	tm, err := s.People.GetTeamMember(ctx, teamID, userID)
	if err != nil {
		return err
	}
	if tm == nil || tm.DeleteAt != 0 {
		return ErrNotTeamMember
	}
	return s.ensureMember(ctx, userID, "")
}

// Me is the user's state as seen from teamID.
func (s *Service) Me(ctx context.Context, userID, teamID string) (MeState, error) {
	if err := s.checkMember(ctx, userID, teamID); err != nil {
		return MeState{}, err
	}
	now := s.Now()
	st := MeState{
		Now: now, UserID: userID,
		Team:    TeamRef{ID: teamID, Name: s.Teams.TeamName(teamID)},
		IsAdmin: s.isAdmin(ctx, teamID, userID),
	}
	if ms, err := s.Store.GetMember(ctx, userID); err != nil {
		return MeState{}, err
	} else if ms != nil {
		st.GitHubLinked = ms.GitHubLogin != ""
	}

	open, err := s.Store.OpenSession(ctx, userID)
	if err != nil {
		return MeState{}, err
	}
	if open != nil {
		ss := &SessionState{
			Team:    TeamRef{ID: open.TeamID, Name: s.Teams.TeamName(open.TeamID)},
			CheckIn: open.CheckIn, Mode: open.WorkMode, OnBreak: onBreak(open),
		}
		if ss.OnBreak {
			start := open.Breaks[len(open.Breaks)-1].Start
			ss.BreakSince = &start
		}
		var net time.Duration
		for _, d := range NetByDay([]Session{*open}, now, s.Loc) {
			net += d
		}
		ss.NetMinutes, ss.NetSeconds = int(net.Minutes()), int(net.Seconds())
		st.Session = ss
	}

	perTeam, err := s.teamTotals(ctx, userID, now, now)
	if err != nil {
		return MeState{}, err
	}
	st.TodayTeamMinutes, st.TodayTeamSeconds = int(perTeam[teamID].Minutes()), int(perTeam[teamID].Seconds())
	st.TodayAllMinutes, st.TodayAllSeconds = int(sum(perTeam).Minutes()), int(sum(perTeam).Seconds())
	return st, nil
}

// Act runs a session action for the panel and returns the same message
// the /att command would show (Mattermost markdown).
func (s *Service) Act(ctx context.Context, userID, teamID, action, text string) (string, error) {
	if err := s.checkMember(ctx, userID, teamID); err != nil {
		return "", err
	}
	var (
		r   Reply
		err error
	)
	switch action {
	case ActionIn:
		r, err = s.checkIn(ctx, Command{TeamID: teamID, UserID: userID}, text)
	case ActionBreak:
		r, err = s.startBreak(ctx, userID)
	case ActionBack:
		r, err = s.endBreak(ctx, userID)
	case ActionOut:
		r, err = s.checkOut(ctx, userID, text, "") // the panel opens its own EOD form
	default:
		return "", errors.New("unknown action")
	}
	if err != nil {
		return "", err
	}
	return r.Text, nil
}

// notify tells the plugin that userID's panel should refresh (best effort).
func (s *Service) notify(userID, event, teamID string) {
	if s.Notify != nil {
		s.Notify(userID, event, teamID)
	}
}
