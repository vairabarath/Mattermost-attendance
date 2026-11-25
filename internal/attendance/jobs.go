package attendance

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/vairabarath/Mattermost-attendance/internal/config"
	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// OpenSessionInfo is an open session with its member, for job messages.
type OpenSessionInfo struct {
	UserID   string
	Username string
	Session  Session // with breaks
	Expected float64 // member's expected hours per day
}

// JobStore is the persistence the scheduled jobs need.
type JobStore interface {
	// ClaimJobRun records job+key and reports false if it already ran.
	ClaimJobRun(ctx context.Context, job, key string) (bool, error)
	// SyncMembers upserts team members seen in Mattermost (registers them lazily).
	SyncMembers(ctx context.Context, users []mattermost.User) error
	AllMembers(ctx context.Context) (map[string]MemberSettings, error)
	// AllSessionsOverlapping returns every user's sessions overlapping [from, to).
	AllSessionsOverlapping(ctx context.Context, from, to time.Time) (map[string][]Session, error)
	// OpenSessions lists open sessions; openedBefore filters (zero = all).
	OpenSessions(ctx context.Context, openedBefore time.Time) ([]OpenSessionInfo, error)
	// AutoCloseSession closes the session at out if it is still open.
	AutoCloseSession(ctx context.Context, sessionID int64, out time.Time) (closedAt time.Time, closed bool, err error)
	HolidaysOn(ctx context.Context, day string) ([]Holiday, error)
	InsertAudit(ctx context.Context, e AuditEntry) error
}

// Messenger posts to channels and DMs members as the bot.
type Messenger interface {
	CreatePost(ctx context.Context, channelID, message string) error
	DirectMessage(ctx context.Context, userID, message string) error
}

// RosterAPI lists the users of a team.
type RosterAPI interface {
	TeamUsers(ctx context.Context, teamID string) ([]mattermost.User, error)
}

// LeaveChecker is the hook for skipping people on leave (spec §16 Q4).
// Leave isn't implemented yet; NoLeave says nobody is on leave.
type LeaveChecker interface {
	OnLeave(ctx context.Context, userID string, day time.Time) bool
}

// NoLeave is the default LeaveChecker.
type NoLeave struct{}

func (NoLeave) OnLeave(context.Context, string, time.Time) bool { return false }

// Jobs runs the scheduled jobs of spec section 9.
type Jobs struct {
	Store      JobStore
	Roster     RosterAPI
	Msg        Messenger
	Status     *StatusUpdater
	Teams      []config.Team
	TeamNames  Teams
	Workdays   map[time.Weekday]bool
	MaxSession time.Duration
	Leave      LeaveChecker
	Loc        *time.Location
	Log        *slog.Logger
	Now        func() time.Time
}

// dailyJob is a job that runs once per day at a fixed local time.
type dailyJob struct {
	name         string
	hour, minute int
	run          func(context.Context, time.Time)
}

func (j *Jobs) daily() []dailyJob {
	return []dailyJob{
		{"reminder", 10, 30, j.Reminders},
		{"still_checked_in", 19, 0, j.StillCheckedIn},
		{"daily_summary", 20, 0, j.DailySummaries},
	}
}

// catchUpWindow: after a restart, a daily job whose time passed less than
// this long ago still runs (job_runs stops it from running twice).
const catchUpWindow = 2 * time.Hour

// Start schedules the jobs and runs anything due now. Stop with the returned func.
func (j *Jobs) Start(ctx context.Context) func() {
	c := cron.New(cron.WithLocation(j.Loc))
	for _, d := range j.daily() {
		d := d
		spec := fmt.Sprintf("%d %d * * *", d.minute, d.hour)
		if _, err := c.AddFunc(spec, func() { d.run(ctx, j.Now()) }); err != nil {
			j.Log.Error("schedule job", "job", d.name, "err", err)
		}
	}
	// Hourly at :05, every day including weekends and holidays.
	if _, err := c.AddFunc("5 * * * *", func() { j.AutoClose(ctx, j.Now()) }); err != nil {
		j.Log.Error("schedule job", "job", "auto_close", "err", err)
	}
	c.Start()

	go j.catchUp(ctx)
	return func() { <-c.Stop().Done() }
}

func (j *Jobs) catchUp(ctx context.Context) {
	now := j.Now()
	j.AutoClose(ctx, now)
	local := now.In(j.Loc)
	for _, d := range j.daily() {
		at := time.Date(local.Year(), local.Month(), local.Day(), d.hour, d.minute, 0, 0, j.Loc)
		if !now.Before(at) && now.Sub(at) < catchUpWindow {
			j.Log.Info("catching up missed job", "job", d.name)
			d.run(ctx, now)
		}
	}
}

// once runs fn only if job+key hasn't run yet. It claims first, so a crash
// mid-run never sends the same messages twice.
func (j *Jobs) once(ctx context.Context, job, key string, fn func(context.Context) error) {
	ok, err := j.Store.ClaimJobRun(ctx, job, key)
	if err != nil {
		j.Log.Error("claim job run", "job", job, "key", key, "err", err)
		return
	}
	if !ok {
		j.Log.Info("job already ran", "job", job, "key", key)
		return
	}
	start := time.Now()
	if err := fn(ctx); err != nil {
		j.Log.Error("job failed", "job", job, "key", key, "err", err)
		return
	}
	j.Log.Info("job done", "job", job, "key", key, "took_ms", time.Since(start).Milliseconds())
}

// --- calendar ---

// IsWorkday reports whether day is a workday for teamID (configured weekday,
// no holiday for that team or for all teams).
func IsWorkday(day time.Time, teamID string, workdays map[time.Weekday]bool, holidays []Holiday) bool {
	if !workdays[day.Weekday()] {
		return false
	}
	for _, h := range holidays {
		if h.TeamID == "" || h.TeamID == teamID {
			return false
		}
	}
	return true
}

// --- rosters ---

// rosters loads each team's filtered members from Mattermost and registers
// anyone not seen before.
func (j *Jobs) rosters(ctx context.Context) (map[string][]RosterMember, map[string]MemberSettings, error) {
	raw := map[string][]mattermost.User{}
	for _, t := range j.Teams {
		users, err := j.Roster.TeamUsers(ctx, t.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("team %s users: %w", t.Name, err)
		}
		var keep []mattermost.User
		for _, u := range users {
			if !u.IsBot && u.DeleteAt == 0 {
				keep = append(keep, u)
			}
		}
		if err := j.Store.SyncMembers(ctx, keep); err != nil {
			return nil, nil, err
		}
		raw[t.ID] = keep
	}
	settings, err := j.Store.AllMembers(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := map[string][]RosterMember{}
	for teamID, users := range raw {
		out[teamID] = FilterRoster(users, settings)
	}
	return out, settings, nil
}

func (j *Jobs) dayKey(now time.Time) string { return now.In(j.Loc).Format(DayFormat) }

// --- 10:30 reminder ---

// Reminders DMs tracked members (reminders on) who haven't checked in to any
// team today. One DM per person, however many teams they're in.
func (j *Jobs) Reminders(ctx context.Context, now time.Time) {
	day := j.dayKey(now)
	j.once(ctx, "reminder", day, func(ctx context.Context) error {
		holidays, err := j.Store.HolidaysOn(ctx, day)
		if err != nil {
			return err
		}
		rosters, settings, err := j.rosters(ctx)
		if err != nil {
			return err
		}
		from, to := DayBounds(now, j.Loc)
		sessions, err := j.Store.AllSessionsOverlapping(ctx, from, to)
		if err != nil {
			return err
		}

		// Only remind people with at least one team that works today.
		due := map[string]bool{}
		for _, t := range j.Teams {
			if !IsWorkday(now.In(j.Loc), t.ID, j.Workdays, holidays) {
				continue
			}
			for _, m := range rosters[t.ID] {
				due[m.UserID] = true
			}
		}
		sent := 0
		for uid := range due {
			ms, ok := settings[uid]
			if !ok || !ms.Tracked || !ms.RemindersOn || len(sessions[uid]) > 0 || j.Leave.OnLeave(ctx, uid, now) {
				continue
			}
			if err := j.Msg.DirectMessage(ctx, uid, "👋 Good morning! You haven't checked in yet today. Run `/att in` in your organization's team when you start."); err != nil {
				j.Log.Warn("reminder DM failed", "user_id", uid, "err", err)
				continue
			}
			sent++
		}
		j.Log.Info("reminders sent", "count", sent)
		return nil
	})
}

// --- 19:00 still checked in ---

// StillCheckedIn DMs everyone with an open session in a team that works today.
func (j *Jobs) StillCheckedIn(ctx context.Context, now time.Time) {
	day := j.dayKey(now)
	j.once(ctx, "still_checked_in", day, func(ctx context.Context) error {
		holidays, err := j.Store.HolidaysOn(ctx, day)
		if err != nil {
			return err
		}
		open, err := j.Store.OpenSessions(ctx, time.Time{})
		if err != nil {
			return err
		}
		for _, o := range open {
			if !IsWorkday(now.In(j.Loc), o.Session.TeamID, j.Workdays, holidays) {
				continue
			}
			msg := fmt.Sprintf("⏰ You're still checked in to **%s** since %s. Run `/att out` when you finish.",
				j.TeamNames.TeamName(o.Session.TeamID), fmtLocalClock(o.Session.CheckIn, now, j.Loc))
			if err := j.Msg.DirectMessage(ctx, o.UserID, msg); err != nil {
				j.Log.Warn("still-checked-in DM failed", "user_id", o.UserID, "err", err)
			}
		}
		return nil
	})
}

// --- 20:00 daily summary ---

// DailySummaries posts each team's summary to its #attendance channel.
func (j *Jobs) DailySummaries(ctx context.Context, now time.Time) {
	day := j.dayKey(now)
	holidays, err := j.Store.HolidaysOn(ctx, day)
	if err != nil {
		j.Log.Error("daily summary: holidays", "err", err)
		return
	}
	var due []config.Team
	for _, t := range j.Teams {
		if t.AttendanceChannelID == "" {
			j.Log.Warn("daily summary: no attendance channel configured", "team", t.Name)
			continue
		}
		if IsWorkday(now.In(j.Loc), t.ID, j.Workdays, holidays) {
			due = append(due, t)
		}
	}
	if len(due) == 0 {
		return
	}
	rosters, settings, err := j.rosters(ctx)
	if err != nil {
		j.Log.Error("daily summary: rosters", "err", err)
		return
	}
	from, to := DayBounds(now, j.Loc)
	sessions, err := j.Store.AllSessionsOverlapping(ctx, from, to)
	if err != nil {
		j.Log.Error("daily summary: sessions", "err", err)
		return
	}
	usernames := map[string]string{}
	for id, ms := range settings {
		usernames[id] = ms.Username
	}
	for _, t := range due {
		t := t
		j.once(ctx, "daily_summary", day+"/"+t.Key, func(ctx context.Context) error {
			msg := BuildDailySummary(SummaryInput{
				TeamID: t.ID, TeamName: t.Name, Teams: j.TeamNames,
				Day: now, Now: now, Loc: j.Loc,
				Roster: rosters[t.ID], Usernames: usernames, Sessions: sessions,
				OnLeave: func(uid string) bool { return j.Leave.OnLeave(ctx, uid, now) },
			})
			return j.Msg.CreatePost(ctx, t.AttendanceChannelID, msg)
		})
	}
}

// --- hourly auto-close ---

// AutoClose closes sessions open longer than MaxSession so that their net
// time equals the member's expected hours (never later than now).
func (j *Jobs) AutoClose(ctx context.Context, now time.Time) {
	key := now.In(j.Loc).Format("2006-01-02T15")
	j.once(ctx, "auto_close", key, func(ctx context.Context) error {
		stale, err := j.Store.OpenSessions(ctx, now.Add(-j.MaxSession))
		if err != nil {
			return err
		}
		for _, o := range stale {
			if err := j.autoCloseOne(ctx, o, now); err != nil {
				j.Log.Error("auto-close failed", "user_id", o.UserID, "session_id", o.Session.ID, "err", err)
			}
		}
		return nil
	})
}

func (j *Jobs) autoCloseOne(ctx context.Context, o OpenSessionInfo, now time.Time) error {
	expected := hoursToDuration(o.Expected)
	out := AutoCloseTime(o.Session, expected, now)
	closedAt, closed, err := j.Store.AutoCloseSession(ctx, o.Session.ID, out)
	if err != nil || !closed {
		return err // !closed: the member checked out meanwhile
	}
	s := o.Session
	s.CheckOut = &closedAt
	var net time.Duration
	for _, v := range NetByDay([]Session{s}, now, j.Loc) {
		net += v
	}
	teamName := j.TeamNames.TeamName(s.TeamID)
	j.Log.Info("auto-closed session", "user_id", o.UserID, "session_id", s.ID, "net_min", int(net.Minutes()))

	if err := j.Store.InsertAudit(ctx, AuditEntry{
		ActorID: "system", TargetID: o.UserID, TeamID: s.TeamID, Action: "auto_close",
		Old:    map[string]any{"session_id": s.ID, "check_in": s.CheckIn, "check_out": nil},
		New:    map[string]any{"session_id": s.ID, "check_in": s.CheckIn, "check_out": closedAt, "net_minutes": int(net.Minutes())},
		Reason: "open longer than " + strconv.Itoa(int(j.MaxSession.Hours())) + "h",
	}); err != nil {
		j.Log.Error("auto-close audit", "err", err)
	}
	j.Status.Enqueue(o.UserID, StatusOff, false)

	since := s.CheckIn.In(j.Loc).Format("2 Jan, 3:04 PM")
	dm := fmt.Sprintf("⏱️ You forgot to check out of **%s** (checked in %s). I closed the session at %s and counted **%s** (your expected hours). If that's wrong, ask an admin to correct it.",
		teamName, since, closedAt.In(j.Loc).Format("2 Jan, 3:04 PM"), FormatDuration(net))
	if err := j.Msg.DirectMessage(ctx, o.UserID, dm); err != nil {
		j.Log.Warn("auto-close DM failed", "user_id", o.UserID, "err", err)
	}
	for _, t := range j.Teams {
		if t.ID == s.TeamID && t.AdminChannelID != "" {
			flag := fmt.Sprintf("⚠️ **Auto-closed:** @%s's session in %s (checked in %s) was open over %dh. Closed at %s, counted %s.",
				o.Username, teamName, since, int(j.MaxSession.Hours()), closedAt.In(j.Loc).Format("2 Jan, 3:04 PM"), FormatDuration(net))
			if err := j.Msg.CreatePost(ctx, t.AdminChannelID, flag); err != nil {
				j.Log.Warn("auto-close admin post failed", "team", t.Name, "err", err)
			}
		}
	}
	return nil
}
