package attendance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// Errors returned by Store implementations.
var (
	ErrAlreadyCheckedIn = errors.New("already checked in")
	ErrNotCheckedIn     = errors.New("not checked in")
)

// Store is the persistence the service needs (implemented by internal/store).
type Store interface {
	// MemberRefreshedAt reports when a member's profile was last synced.
	MemberRefreshedAt(ctx context.Context, userID string) (t time.Time, found bool, err error)
	UpsertMember(ctx context.Context, userID, username, displayName string) error
	// InsertSession returns ErrAlreadyCheckedIn if the user has any open session.
	InsertSession(ctx context.Context, userID, teamID string, checkIn time.Time, mode, note string) (int64, error)
	// OpenSession returns the user's open session, or nil.
	OpenSession(ctx context.Context, userID string) (*Session, error)
	// CloseOpenSession closes the open session (and any open break) at out.
	// Returns ErrNotCheckedIn if there is none.
	CloseOpenSession(ctx context.Context, userID string, out time.Time, summary string) (*Session, error)
	// SessionsOverlapping returns the user's sessions (with breaks) that
	// overlap [from, to).
	SessionsOverlapping(ctx context.Context, userID string, from, to time.Time) ([]Session, error)
}

// Directory looks up Mattermost user profiles.
type Directory interface {
	UserProfile(ctx context.Context, userID string) (username, displayName string, err error)
}

// Teams resolves configured team ids to names.
type Teams interface {
	TeamName(teamID string) string
}

// Command is a verified slash command invocation.
type Command struct {
	TeamID   string
	UserID   string
	UserName string
	Text     string // everything after "/att"
}

// Service implements the /att commands.
type Service struct {
	Store     Store
	Directory Directory
	Teams     Teams
	Loc       *time.Location
	Log       *slog.Logger
	Now       func() time.Time // overridable for tests
}

const (
	maxNoteLen      = 500
	profileMaxAge   = 24 * time.Hour
	genericErrorMsg = "⚠️ Something went wrong. Please try again, or tell an admin if it keeps happening."
)

// Handle routes a command and returns the ephemeral reply text.
func (s *Service) Handle(ctx context.Context, c Command) string {
	sub, rest := splitFirst(c.Text)
	log := s.Log.With("team_id", c.TeamID, "user_id", c.UserID, "sub", sub)

	if sub == "" || sub == "help" {
		return helpText()
	}

	if err := s.ensureMember(ctx, c); err != nil {
		log.Error("register member", "err", err)
		return genericErrorMsg
	}

	var (
		reply string
		err   error
	)
	switch sub {
	case "in":
		reply, err = s.checkIn(ctx, c, rest)
	case "out":
		reply, err = s.checkOut(ctx, c, rest)
	case "hours":
		reply, err = s.hours(ctx, c, rest)
	default:
		return fmt.Sprintf("Unknown command `%s`.\n\n%s", sub, helpText())
	}
	if err != nil {
		log.Error("command failed", "err", err)
		return genericErrorMsg
	}
	log.Info("command ok")
	return reply
}

// ensureMember registers the user on first use and refreshes their profile
// at most once a day. Keyed on user id, never username.
func (s *Service) ensureMember(ctx context.Context, c Command) error {
	refreshed, found, err := s.Store.MemberRefreshedAt(ctx, c.UserID)
	if err != nil {
		return err
	}
	if found && s.Now().Sub(refreshed) < profileMaxAge {
		return nil
	}
	username, display, err := s.Directory.UserProfile(ctx, c.UserID)
	if err != nil {
		s.Log.Warn("profile fetch failed; using slash payload", "user_id", c.UserID, "err", err)
		if found {
			return nil // keep the stored profile, retry next time
		}
		username, display = c.UserName, ""
	}
	return s.Store.UpsertMember(ctx, c.UserID, username, display)
}

func (s *Service) checkIn(ctx context.Context, c Command, note string) (string, error) {
	now := s.Now()
	note = truncate(note, maxNoteLen)
	// Work mode is always office until WFH approvals exist (phase 5).
	_, err := s.Store.InsertSession(ctx, c.UserID, c.TeamID, now, "office", note)
	if errors.Is(err, ErrAlreadyCheckedIn) {
		open, oerr := s.Store.OpenSession(ctx, c.UserID)
		if oerr != nil || open == nil {
			return "You're already checked in.", oerr
		}
		return fmt.Sprintf("You're already checked in to **%s** since %s. Use `/att out` first.",
			s.Teams.TeamName(open.TeamID), s.fmtSince(open.CheckIn, now)), nil
	}
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("✅ Checked in to **%s** at %s · 🏢 Office", s.Teams.TeamName(c.TeamID), s.fmtClock(now))
	if note != "" {
		msg += "\n> " + note
	}
	return msg, nil
}

func (s *Service) checkOut(ctx context.Context, c Command, summary string) (string, error) {
	now := s.Now()
	sess, err := s.Store.CloseOpenSession(ctx, c.UserID, now, truncate(summary, maxNoteLen))
	if errors.Is(err, ErrNotCheckedIn) {
		return "You're not checked in. Use `/att in` to start.", nil
	}
	if err != nil {
		return "", err
	}
	sessionNet := NetByDay([]Session{*sess}, now, s.Loc)
	var sessionTotal time.Duration
	for _, v := range sessionNet {
		sessionTotal += v
	}

	perTeam, err := s.todayTotals(ctx, c.UserID, now)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("🔴 Checked out of **%s** at %s · this session %s\n%s",
		s.Teams.TeamName(sess.TeamID), s.fmtClock(now), FormatDuration(sessionTotal),
		s.todayLine(perTeam)), nil
}

func (s *Service) hours(ctx context.Context, c Command, arg string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "", "today":
	case "week", "month":
		return "`/att hours week|month` isn't available yet. Use `/att hours today` for now.", nil
	default:
		return "Usage: `/att hours [today|week|month]`", nil
	}
	now := s.Now()
	perTeam, err := s.todayTotals(ctx, c.UserID, now)
	if err != nil {
		return "", err
	}
	open, err := s.Store.OpenSession(ctx, c.UserID)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "#### Your hours today (%s)\n", now.In(s.Loc).Format("Mon 2 Jan"))
	if len(perTeam) == 0 {
		b.WriteString("No time recorded today.")
	} else {
		b.WriteString("| Organization | Hours |\n|---|---|\n")
		for _, tl := range sortedTeams(perTeam, s.Teams) {
			fmt.Fprintf(&b, "| %s | %s |\n", tl.name, FormatDuration(tl.d))
		}
		fmt.Fprintf(&b, "| **Total** | **%s** |", FormatDuration(sum(perTeam)))
	}
	if open != nil {
		fmt.Fprintf(&b, "\n\n🟢 Currently checked in to **%s** since %s.",
			s.Teams.TeamName(open.TeamID), s.fmtSince(open.CheckIn, now))
	}
	return b.String(), nil
}

// todayTotals returns net time per team for the current local day.
func (s *Service) todayTotals(ctx context.Context, userID string, now time.Time) (map[string]time.Duration, error) {
	start, end := DayBounds(now, s.Loc)
	sessions, err := s.Store.SessionsOverlapping(ctx, userID, start, end)
	if err != nil {
		return nil, err
	}
	return TeamTotalsForDay(NetByDay(sessions, now, s.Loc), now.In(s.Loc).Format(DayFormat)), nil
}

func (s *Service) todayLine(perTeam map[string]time.Duration) string {
	parts := make([]string, 0, len(perTeam))
	for _, tl := range sortedTeams(perTeam, s.Teams) {
		parts = append(parts, fmt.Sprintf("%s %s", tl.name, FormatDuration(tl.d)))
	}
	line := "Today total: **" + FormatDuration(sum(perTeam)) + "**"
	if len(parts) > 1 {
		line += " (" + strings.Join(parts, ", ") + ")"
	}
	return line
}

func (s *Service) fmtClock(t time.Time) string { return t.In(s.Loc).Format("3:04 PM") }

// fmtSince shows only the time for today, otherwise date and time.
func (s *Service) fmtSince(t, now time.Time) string {
	if t.In(s.Loc).Format(DayFormat) == now.In(s.Loc).Format(DayFormat) {
		return s.fmtClock(t)
	}
	return t.In(s.Loc).Format("2 Jan, 3:04 PM")
}

type teamDur struct {
	name string
	d    time.Duration
}

func sortedTeams(m map[string]time.Duration, teams Teams) []teamDur {
	out := make([]teamDur, 0, len(m))
	for id, d := range m {
		out = append(out, teamDur{teams.TeamName(id), d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func sum(m map[string]time.Duration) time.Duration {
	var t time.Duration
	for _, v := range m {
		t += v
	}
	return t
}

func splitFirst(s string) (string, string) {
	s = strings.TrimSpace(s)
	first, rest, _ := strings.Cut(s, " ")
	return strings.ToLower(first), strings.TrimSpace(rest)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func helpText() string {
	return strings.Join([]string{
		"#### Attendance commands",
		"| Command | What it does |",
		"|---|---|",
		"| `/att in [note]` | Check in to this team |",
		"| `/att out [summary]` | Check out and see today's total |",
		"| `/att hours [today]` | Your hours today, per organization |",
		"| `/att help` | This list |",
	}, "\n")
}
