package attendance

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// Errors from FixDay.
var (
	ErrOpenSessionOnDay = errors.New("open session on that day")
	ErrFixOverlap       = errors.New("fix overlaps another session")
)

// FixRequest replaces a member's sessions in one team on one local day.
type FixRequest struct {
	ActorID, UserID, TeamID string
	DayStart, DayEnd        time.Time // the local day's bounds
	In, Out                 time.Time
	Mode, Reason            string
}

// FixResult is what FixDay replaced.
type FixResult struct {
	Old   []Session
	NewID int64
}

// ExportRow is one session for the CSV export.
type ExportRow struct {
	Username, DisplayName string
	Session               Session
}

// ReportStore is the persistence for reports, fixes and exports.
type ReportStore interface {
	rosterStore
	AllSessionsOverlapping(ctx context.Context, from, to time.Time) (map[string][]Session, error)
	HolidaysBetween(ctx context.Context, fromDay, toDay string) ([]Holiday, error)
	// FixDay deletes the member's closed sessions in the team that started
	// on the day, inserts one new session and writes the audit row, in one
	// transaction. Returns ErrOpenSessionOnDay or ErrFixOverlap.
	FixDay(ctx context.Context, r FixRequest) (FixResult, error)
	// TeamSessionsBetween returns the team's sessions that started in [from, to).
	TeamSessionsBetween(ctx context.Context, teamID string, from, to time.Time) ([]ExportRow, error)
}

// FileSender DMs a file to a member.
type FileSender interface {
	DirectFile(ctx context.Context, userID, message, filename string, data []byte) error
}

// --- /att report ---

const reportUsage = "Usage:\n" +
	"- `/att report @user [week|month]` — a member's hours in this team\n" +
	"- `/att report team [week|month]` — every member of this team"

func (s *Service) report(ctx context.Context, c Command, args string) (Reply, error) {
	if r, err := s.requireAdmin(ctx, c.TeamID, c.UserID); r != nil || err != nil {
		return deref(r), err
	}
	who, rest := splitFirst(args)
	periodName := strings.ToLower(strings.TrimSpace(rest))
	if periodName == "" {
		periodName = "week"
	}
	now := s.Now()
	p, ok := PeriodFor(periodName, now, s.Loc)
	if who == "" || !ok {
		return text(reportUsage), nil
	}
	if who == "team" {
		return s.teamReport(ctx, c.TeamID, p, now)
	}
	m := usernameRe.FindStringSubmatch(who)
	if m == nil {
		return text(reportUsage), nil
	}
	target, err := s.People.GetUserByUsername(ctx, m[1])
	if err != nil {
		return Reply{}, err
	}
	if target == nil || target.IsBot {
		return text(fmt.Sprintf("No member `@%s`.", m[1])), nil
	}
	return s.userReport(ctx, c.TeamID, target, p, now)
}

func (s *Service) periodBounds(p Period) (time.Time, time.Time) {
	from, _ := DayBounds(p.FirstDay, s.Loc)
	_, to := DayBounds(p.LastDay, s.Loc)
	return from, to
}

func (s *Service) userReport(ctx context.Context, teamID string, u *mattermost.User, p Period, now time.Time) (Reply, error) {
	from, to := s.periodBounds(p)
	sessions, err := s.Store.SessionsOverlapping(ctx, u.ID, from, to)
	if err != nil {
		return Reply{}, err
	}
	holidays, err := s.Store.HolidaysBetween(ctx, p.FirstDay.Format(DayFormat), p.LastDay.Format(DayFormat))
	if err != nil {
		return Reply{}, err
	}
	ms, err := s.Store.GetMember(ctx, u.ID)
	if err != nil {
		return Reply{}, err
	}

	team := s.Teams.TeamName(teamID)
	var b strings.Builder
	fmt.Fprintf(&b, "#### @%s in %s — %s (%s – %s)\n", u.Username, team, p.Name,
		p.FirstDay.Format("Mon 2 Jan"), p.LastDay.Format("Mon 2 Jan"))
	rows := BuildUserReport(sessions, teamID, p, now, s.Loc)
	var total time.Duration
	if len(rows) == 0 {
		fmt.Fprintf(&b, "No time recorded in %s.\n", team)
	} else {
		b.WriteString("| Day | Hours | Mode | Breaks | Note |\n|---|---|---|---|---|\n")
		for _, r := range rows {
			d, _ := time.ParseInLocation(DayFormat, r.Day, s.Loc)
			var note []string
			if r.AutoClosed {
				note = append(note, "⏱️ auto-closed")
			}
			if r.Open {
				note = append(note, "🟢 open")
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", d.Format("Mon 2 Jan"), FormatDuration(r.Net),
				modesIcons(r.Modes), FormatDuration(r.Breaks), strings.Join(note, ", "))
			total += r.Net
		}
		fmt.Fprintf(&b, "| **Total** | **%s** | | | |\n", FormatDuration(total))
	}

	var all time.Duration
	for _, v := range inPeriod(NetByDay(sessions, now, s.Loc), p) {
		all += v
	}
	line := "\nAll orgs this period: **" + FormatDuration(all) + "**"
	if ms == nil || ms.Tracked {
		perDay := 8.0
		if ms != nil {
			perDay = ms.ExpectedHours
		}
		line += " · Expected: " + FormatDuration(ExpectedHours(perDay, p, teamID, s.Workdays, holidays))
	} else {
		line += " · not tracked"
	}
	b.WriteString(line)
	return text(b.String()), nil
}

func (s *Service) teamReport(ctx context.Context, teamID string, p Period, now time.Time) (Reply, error) {
	in, err := s.teamReportInput(ctx, teamID, p, now)
	if err != nil {
		return Reply{}, err
	}
	title := fmt.Sprintf("#### %s — %s (%s – %s)", s.Teams.TeamName(teamID), p.Name,
		p.FirstDay.Format("Mon 2 Jan"), p.LastDay.Format("Mon 2 Jan"))
	return text(RenderTeamReport(title, BuildTeamReport(in))), nil
}

// teamReportInput loads the roster, sessions and holidays for a team report.
func (s *Service) teamReportInput(ctx context.Context, teamID string, p Period, now time.Time) (TeamReportInput, error) {
	return buildTeamReportInput(ctx, s.Roster, s.Store, teamID, p, now, s.Loc, s.Workdays)
}

// teamReportStore is what a team report reads.
type teamReportStore interface {
	rosterStore
	AllSessionsOverlapping(ctx context.Context, from, to time.Time) (map[string][]Session, error)
	HolidaysBetween(ctx context.Context, fromDay, toDay string) ([]Holiday, error)
}

func buildTeamReportInput(ctx context.Context, api RosterAPI, st teamReportStore, teamID string, p Period,
	now time.Time, loc *time.Location, workdays map[time.Weekday]bool) (TeamReportInput, error) {
	rosters, settings, err := loadRosters(ctx, api, st, []string{teamID})
	if err != nil {
		return TeamReportInput{}, err
	}
	from, _ := DayBounds(p.FirstDay, loc)
	_, to := DayBounds(p.LastDay, loc)
	sessions, err := st.AllSessionsOverlapping(ctx, from, to)
	if err != nil {
		return TeamReportInput{}, err
	}
	holidays, err := st.HolidaysBetween(ctx, p.FirstDay.Format(DayFormat), p.LastDay.Format(DayFormat))
	if err != nil {
		return TeamReportInput{}, err
	}
	usernames := map[string]string{}
	for id, ms := range settings {
		usernames[id] = ms.Username
	}
	return TeamReportInput{
		TeamID: teamID, Period: p, Now: now, Loc: loc,
		Roster: rosters[teamID], Usernames: usernames, Settings: settings,
		Sessions: sessions, Workdays: workdays, Holidays: holidays,
	}, nil
}

// --- /att fix ---

const fixUsage = "Usage: `/att fix @user YYYY-MM-DD in HH:MM out HH:MM <reason>`\n" +
	"Replaces all of that member's sessions **in this team** that started on that date with one session (no breaks). Times are 24-hour, " +
	"same date. A reason is required and everything is written to the audit log."

var fixRe = regexp.MustCompile(`(?i)^@?([a-z0-9._-]{1,64})\s+(\d{4}-\d{2}-\d{2})\s+in\s+(\d{1,2}:\d{2})\s+out\s+(\d{1,2}:\d{2})\s+(.+)$`)

// FixArgs is a parsed /att fix command.
type FixArgs struct {
	Username     string
	Day, In, Out time.Time // Day = local midnight
	Reason       string
}

// ParseFix parses and validates /att fix arguments (spec: `/att fix` rules).
// It returns a user-facing message when invalid.
func ParseFix(args string, now time.Time, loc *time.Location) (FixArgs, string) {
	m := fixRe.FindStringSubmatch(strings.TrimSpace(args))
	if m == nil {
		return FixArgs{}, fixUsage
	}
	// Only the username is case-insensitive; keep the reason as typed.
	day, err := time.ParseInLocation(DayFormat, m[2], loc)
	if err != nil {
		return FixArgs{}, "Invalid date `" + m[2] + "`."
	}
	clock := func(hhmm string) (time.Time, bool) {
		h, mm, _ := strings.Cut(hhmm, ":")
		hi, err1 := strconv.Atoi(h)
		mi, err2 := strconv.Atoi(mm)
		if err1 != nil || err2 != nil || hi > 23 || mi > 59 {
			return time.Time{}, false
		}
		return time.Date(day.Year(), day.Month(), day.Day(), hi, mi, 0, 0, loc), true
	}
	in, ok1 := clock(m[3])
	out, ok2 := clock(m[4])
	if !ok1 || !ok2 {
		return FixArgs{}, "Times must be HH:MM (24-hour), e.g. `09:30` and `18:00`."
	}
	today, _ := DayBounds(now, loc)
	switch {
	case day.After(today):
		return FixArgs{}, "Can't fix a future date."
	case !out.After(in):
		return FixArgs{}, "`out` must be after `in` on the same date (overnight corrections aren't supported)."
	case out.After(now):
		return FixArgs{}, "`out` is in the future."
	}
	reason := truncate(m[5], maxNoteLen)
	if len([]rune(reason)) < 3 {
		return FixArgs{}, "Please give a reason."
	}
	return FixArgs{Username: strings.ToLower(m[1]), Day: day, In: in, Out: out, Reason: reason}, ""
}

func (s *Service) fix(ctx context.Context, c Command, args string) (Reply, error) {
	if r, err := s.requireAdmin(ctx, c.TeamID, c.UserID); r != nil || err != nil {
		return deref(r), err
	}
	fa, msg := ParseFix(lowerUsername(args), s.Now(), s.Loc)
	if msg != "" {
		return text(msg), nil
	}
	target, err := s.People.GetUserByUsername(ctx, fa.Username)
	if err != nil {
		return Reply{}, err
	}
	if target == nil || target.IsBot {
		return text(fmt.Sprintf("No member `@%s`.", fa.Username)), nil
	}
	if err := s.Store.UpsertMember(ctx, target.ID, target.Username, target.DisplayName()); err != nil {
		return Reply{}, err
	}
	dayStart, dayEnd := DayBounds(fa.Day, s.Loc)
	res, err := s.Store.FixDay(ctx, FixRequest{
		ActorID: c.UserID, UserID: target.ID, TeamID: c.TeamID,
		DayStart: dayStart, DayEnd: dayEnd, In: fa.In, Out: fa.Out,
		Mode:   "office", // WFH approvals set this from phase 5
		Reason: fa.Reason,
	})
	switch {
	case errors.Is(err, ErrOpenSessionOnDay):
		return text(fmt.Sprintf("@%s is still checked in to **%s** from that day. Ask them to `/att out` first.",
			target.Username, s.Teams.TeamName(c.TeamID))), nil
	case errors.Is(err, ErrFixOverlap):
		return text(fmt.Sprintf("%s–%s overlaps another of @%s's sessions (another organization, or one crossing midnight). Fix that one first or choose other times.",
			fa.In.Format("15:04"), fa.Out.Format("15:04"), target.Username)), nil
	case err != nil:
		return Reply{}, err
	}
	var oldTotal time.Duration
	for _, v := range NetByDay(res.Old, s.Now(), s.Loc) {
		oldTotal += v
	}
	return text(fmt.Sprintf("🛠️ Fixed @%s in **%s** on %s: %s–%s (%s). Replaced %d session(s) totalling %s. Reason: %s",
		target.Username, s.Teams.TeamName(c.TeamID), fa.Day.Format("Mon 2 Jan"),
		fa.In.Format("3:04 PM"), fa.Out.Format("3:04 PM"), FormatDuration(fa.Out.Sub(fa.In)),
		len(res.Old), FormatDuration(oldTotal), fa.Reason)), nil
}

// lowerUsername lower-cases only the first word (the @user).
func lowerUsername(args string) string {
	args = strings.TrimSpace(args)
	first, rest, _ := strings.Cut(args, " ")
	return strings.ToLower(first) + " " + rest
}

// --- /att export ---

func (s *Service) export(ctx context.Context, c Command, args string) (Reply, error) {
	if r, err := s.requireAdmin(ctx, c.TeamID, c.UserID); r != nil || err != nil {
		return deref(r), err
	}
	now := s.Now().In(s.Loc)
	month := strings.TrimSpace(args)
	if month == "" {
		month = now.Format("2006-01")
	}
	start, err := time.ParseInLocation("2006-01", month, s.Loc)
	if err != nil {
		return text("Usage: `/att export [YYYY-MM]` (default: this month)."), nil
	}
	if start.After(now) {
		return text("That month hasn't started yet."), nil
	}
	end := start.AddDate(0, 1, 0)
	rows, err := s.Store.TeamSessionsBetween(ctx, c.TeamID, start, end)
	if err != nil {
		return Reply{}, err
	}
	data, err := ExportCSV(rows, s.Now(), s.Loc)
	if err != nil {
		return Reply{}, err
	}
	team := s.Teams.TeamName(c.TeamID)
	filename := fmt.Sprintf("attendance-%s-%s.csv", slug(team), month)
	msg := fmt.Sprintf("📎 Attendance export for **%s**, %s: %d session(s).", team, start.Format("January 2006"), len(rows))
	if s.Files == nil {
		return text("File export isn't configured."), nil
	}
	if err := s.Files.DirectFile(ctx, c.UserID, msg, filename, data); err != nil {
		return Reply{}, err
	}
	return text(fmt.Sprintf("📎 Sent `%s` (%d sessions) to you in a direct message from @attendance.", filename, len(rows))), nil
}

// ExportCSV renders sessions as CSV. Times are local (TIMEZONE).
func ExportCSV(rows []ExportRow, now time.Time, loc *time.Location) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"username", "display_name", "date", "check_in", "check_out", "break_minutes",
		"net_minutes", "net_hours", "work_mode", "auto_closed", "in_note", "out_summary"})
	for _, r := range rows {
		s := r.Session
		var net time.Duration
		for _, v := range NetByDay([]Session{s}, now, loc) {
			net += v
		}
		gross := s
		gross.Breaks = nil
		var span time.Duration
		for _, v := range NetByDay([]Session{gross}, now, loc) {
			span += v
		}
		out := ""
		if s.CheckOut != nil {
			out = s.CheckOut.In(loc).Format("2006-01-02 15:04")
		}
		_ = w.Write([]string{
			csvSafe(r.Username), csvSafe(r.DisplayName),
			s.CheckIn.In(loc).Format(DayFormat),
			s.CheckIn.In(loc).Format("2006-01-02 15:04"), out,
			strconv.Itoa(int((span - net).Minutes())),
			strconv.Itoa(int(net.Minutes())), FormatDuration(net),
			s.WorkMode, strconv.FormatBool(s.AutoClosed),
			csvSafe(s.InNote), csvSafe(s.OutSummary),
		})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// csvSafe stops spreadsheet formula injection from member-typed text.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
