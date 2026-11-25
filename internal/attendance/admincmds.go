package attendance

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// ErrHolidayExists is returned when the same day/team holiday already exists.
var ErrHolidayExists = errors.New("holiday exists")

// MemberSettings are the per-member attendance settings (global, not per team).
type MemberSettings struct {
	UserID        string  `json:"-"`
	Username      string  `json:"-"`
	DisplayName   string  `json:"-"`
	ExpectedHours float64 `json:"expected_hours"`
	RemindersOn   bool    `json:"reminders_on"`
	Tracked       bool    `json:"tracked"`
	Active        bool    `json:"-"`
	// Since is when the bot first saw the member; expected hours start then.
	Since time.Time `json:"-"`
}

// MemberUpdate changes the non-nil settings.
type MemberUpdate struct {
	Tracked       *bool
	RemindersOn   *bool
	ExpectedHours *float64
}

// Holiday is a day off; TeamID "" means all organizations.
type Holiday struct {
	ID     int64
	Day    string // YYYY-MM-DD
	TeamID string
	Name   string
}

// AuditEntry is one audit_log row. Old/New are stored as JSON.
type AuditEntry struct {
	ActorID, TargetID, TeamID, Action string
	Old, New                          any
	Reason                            string
}

// AdminStore is the persistence for admin commands.
type AdminStore interface {
	GetMember(ctx context.Context, userID string) (*MemberSettings, error)
	UpdateMember(ctx context.Context, userID string, upd MemberUpdate) (before, after MemberSettings, err error)
	InsertAudit(ctx context.Context, e AuditEntry) error
	// AddHoliday returns ErrHolidayExists for a duplicate day+team.
	AddHoliday(ctx context.Context, day, teamID, name string) (int64, error)
	// ListHolidays returns holidays from fromDay on for teamID plus global ones.
	ListHolidays(ctx context.Context, teamID, fromDay string) ([]Holiday, error)
	GetHoliday(ctx context.Context, id int64) (*Holiday, error)
	DeleteHoliday(ctx context.Context, id int64) error
}

// PeopleAPI looks up users and team memberships for admin commands.
type PeopleAPI interface {
	RoleAPI
	GetUserByUsername(ctx context.Context, username string) (*mattermost.User, error)
}

var (
	usernameRe = regexp.MustCompile(`^@?([a-z0-9._-]{1,64})$`)
	dayRe      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

const notAdminMsg = "🔒 Only admins of this team can use that command."

// requireAdmin returns a reply to send if the user is not an admin.
func (s *Service) requireAdmin(ctx context.Context, teamID, userID string) (*Reply, error) {
	if s.Admins == nil {
		r := text(notAdminMsg)
		return &r, nil
	}
	ok, err := s.Admins.IsAdmin(ctx, teamID, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		r := text(notAdminMsg)
		return &r, nil
	}
	return nil, nil
}

// --- /att member ---

const memberUsage = "Usage:\n" +
	"- `/att member @user` — show settings\n" +
	"- `/att member @user tracked on|off` — include in reminders and summaries\n" +
	"- `/att member @user reminders on|off` — 10:30 reminder DM\n" +
	"- `/att member @user hours <n>` — expected hours per day (all organizations combined)"

func (s *Service) member(ctx context.Context, c Command, args string) (Reply, error) {
	if r, err := s.requireAdmin(ctx, c.TeamID, c.UserID); r != nil || err != nil {
		return deref(r), err
	}
	fields := strings.Fields(args)
	if len(fields) != 1 && len(fields) != 3 {
		return text(memberUsage), nil
	}
	m := usernameRe.FindStringSubmatch(strings.ToLower(fields[0]))
	if m == nil {
		return text(memberUsage), nil
	}
	target, err := s.People.GetUserByUsername(ctx, m[1])
	if err != nil {
		return Reply{}, err
	}
	if target == nil {
		return text(fmt.Sprintf("No user `@%s`.", m[1])), nil
	}
	if target.IsBot {
		return text(fmt.Sprintf("`@%s` is a bot; bots are never tracked.", target.Username)), nil
	}
	tm, err := s.People.GetTeamMember(ctx, c.TeamID, target.ID)
	if err != nil {
		return Reply{}, err
	}
	if tm == nil {
		return text(fmt.Sprintf("`@%s` isn't a member of **%s**.", target.Username, s.Teams.TeamName(c.TeamID))), nil
	}
	if err := s.Store.UpsertMember(ctx, target.ID, target.Username, target.DisplayName()); err != nil {
		return Reply{}, err
	}

	if len(fields) == 1 {
		ms, err := s.Store.GetMember(ctx, target.ID)
		if err != nil || ms == nil {
			return Reply{}, err
		}
		return text(fmt.Sprintf("#### Settings for @%s\n| Setting | Value |\n|---|---|\n| Tracked | %s |\n| Reminders | %s |\n| Expected hours/day | %s |\n\nSettings apply across all organizations.",
			target.Username, onOff(ms.Tracked), onOff(ms.RemindersOn), fmtHours(ms.ExpectedHours))), nil
	}

	upd, desc, errMsg := parseMemberUpdate(fields[1], fields[2])
	if errMsg != "" {
		return text(errMsg + "\n\n" + memberUsage), nil
	}
	before, after, err := s.Store.UpdateMember(ctx, target.ID, upd)
	if err != nil {
		return Reply{}, err
	}
	if err := s.Store.InsertAudit(ctx, AuditEntry{
		ActorID: c.UserID, TargetID: target.ID, TeamID: c.TeamID,
		Action: "member_settings", Old: before, New: after,
	}); err != nil {
		return Reply{}, err
	}
	return text(fmt.Sprintf("✅ @%s: %s. Settings apply across all organizations.", target.Username, desc)), nil
}

// parseMemberUpdate turns "tracked off" etc. into an update.
func parseMemberUpdate(setting, value string) (MemberUpdate, string, string) {
	setting, value = strings.ToLower(setting), strings.ToLower(value)
	switch setting {
	case "tracked", "reminders":
		var on bool
		switch value {
		case "on", "yes", "true":
			on = true
		case "off", "no", "false":
		default:
			return MemberUpdate{}, "", fmt.Sprintf("`%s` must be `on` or `off`.", setting)
		}
		if setting == "tracked" {
			return MemberUpdate{Tracked: &on}, "tracked " + onOff(on), ""
		}
		return MemberUpdate{RemindersOn: &on}, "reminders " + onOff(on), ""
	case "hours":
		h, err := strconv.ParseFloat(value, 64)
		if err != nil || h < 0 || h > 24 {
			return MemberUpdate{}, "", "Hours must be a number from 0 to 24, e.g. `8` or `4.5`."
		}
		h = float64(int(h*4+0.5)) / 4 // quarter hours
		return MemberUpdate{ExpectedHours: &h}, "expected hours " + fmtHours(h) + "/day", ""
	}
	return MemberUpdate{}, "", fmt.Sprintf("Unknown setting `%s`.", setting)
}

// --- /att holiday ---

const holidayUsage = "Usage:\n" +
	"- `/att holiday list` — upcoming holidays for this team\n" +
	"- `/att holiday add YYYY-MM-DD <name>` — holiday for this team (admin)\n" +
	"- `/att holiday add YYYY-MM-DD all <name>` — holiday for all organizations (system admin)\n" +
	"- `/att holiday remove <id>` (admin)"

func (s *Service) holiday(ctx context.Context, c Command, args string) (Reply, error) {
	sub, rest := splitFirst(args)
	switch sub {
	case "", "list":
		return s.holidayList(ctx, c.TeamID)
	case "add":
		return s.holidayAdd(ctx, c, rest)
	case "remove", "rm", "delete":
		return s.holidayRemove(ctx, c, rest)
	}
	return text(holidayUsage), nil
}

func (s *Service) holidayList(ctx context.Context, teamID string) (Reply, error) {
	today := s.Now().In(s.Loc).Format(DayFormat)
	hs, err := s.Store.ListHolidays(ctx, teamID, today)
	if err != nil {
		return Reply{}, err
	}
	if len(hs) == 0 {
		return text("No upcoming holidays for **" + s.Teams.TeamName(teamID) + "**.\n\n" + holidayUsage), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#### Upcoming holidays — %s\n| ID | Date | Name | Applies to |\n|---|---|---|---|\n", s.Teams.TeamName(teamID))
	for _, h := range hs {
		scope := s.Teams.TeamName(h.TeamID)
		if h.TeamID == "" {
			scope = "All organizations"
		}
		d, _ := time.ParseInLocation(DayFormat, h.Day, s.Loc)
		fmt.Fprintf(&b, "| %d | %s | %s | %s |\n", h.ID, d.Format("Mon 2 Jan 2006"), h.Name, scope)
	}
	return text(strings.TrimRight(b.String(), "\n")), nil
}

func (s *Service) holidayAdd(ctx context.Context, c Command, args string) (Reply, error) {
	if r, err := s.requireAdmin(ctx, c.TeamID, c.UserID); r != nil || err != nil {
		return deref(r), err
	}
	day, rest := splitFirst(args)
	d, err := time.ParseInLocation(DayFormat, day, s.Loc)
	if !dayRe.MatchString(day) || err != nil {
		return text("Give the date as `YYYY-MM-DD`.\n\n" + holidayUsage), nil
	}
	if day < s.Now().In(s.Loc).Format(DayFormat) {
		return text("That date is in the past."), nil
	}
	teamID, scope := c.TeamID, s.Teams.TeamName(c.TeamID)
	if first, after := splitFirst(rest); first == "all" {
		sys, err := s.Admins.IsSystemAdmin(ctx, c.TeamID, c.UserID)
		if err != nil {
			return Reply{}, err
		}
		if !sys {
			return text("🔒 Only system admins can add a holiday for all organizations."), nil
		}
		teamID, scope, rest = "", "all organizations", after
	}
	name := truncate(rest, 100)
	if name == "" {
		return text("Give the holiday a name.\n\n" + holidayUsage), nil
	}
	id, err := s.Store.AddHoliday(ctx, day, teamID, name)
	if errors.Is(err, ErrHolidayExists) {
		return text(fmt.Sprintf("There's already a holiday on %s for %s.", d.Format("2 Jan 2006"), scope)), nil
	}
	if err != nil {
		return Reply{}, err
	}
	if err := s.Store.InsertAudit(ctx, AuditEntry{
		ActorID: c.UserID, TargetID: "holiday:" + strconv.FormatInt(id, 10), TeamID: teamID,
		Action: "holiday_add", New: Holiday{ID: id, Day: day, TeamID: teamID, Name: name},
	}); err != nil {
		return Reply{}, err
	}
	return text(fmt.Sprintf("🎉 Holiday #%d added: **%s** on %s for %s. Reminders and summaries are skipped that day.",
		id, name, d.Format("Mon 2 Jan 2006"), scope)), nil
}

func (s *Service) holidayRemove(ctx context.Context, c Command, args string) (Reply, error) {
	if r, err := s.requireAdmin(ctx, c.TeamID, c.UserID); r != nil || err != nil {
		return deref(r), err
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(args), "#"), 10, 64)
	if err != nil {
		return text("Usage: `/att holiday remove <id>` (ids are in `/att holiday list`)."), nil
	}
	h, err := s.Store.GetHoliday(ctx, id)
	if err != nil {
		return Reply{}, err
	}
	if h == nil || (h.TeamID != "" && h.TeamID != c.TeamID) {
		return text(fmt.Sprintf("No holiday #%d for this team.", id)), nil
	}
	if h.TeamID == "" {
		sys, err := s.Admins.IsSystemAdmin(ctx, c.TeamID, c.UserID)
		if err != nil {
			return Reply{}, err
		}
		if !sys {
			return text("🔒 Only system admins can remove a holiday for all organizations."), nil
		}
	}
	if err := s.Store.DeleteHoliday(ctx, id); err != nil {
		return Reply{}, err
	}
	if err := s.Store.InsertAudit(ctx, AuditEntry{
		ActorID: c.UserID, TargetID: "holiday:" + strconv.FormatInt(id, 10), TeamID: h.TeamID,
		Action: "holiday_remove", Old: h,
	}); err != nil {
		return Reply{}, err
	}
	return text(fmt.Sprintf("🗑️ Holiday #%d (%s, %s) removed.", id, h.Name, h.Day)), nil
}

func deref(r *Reply) Reply {
	if r == nil {
		return Reply{}
	}
	return *r
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// fmtHours prints 8 as "8h" and 4.5 as "4h 30m".
func fmtHours(h float64) string {
	return strings.TrimSuffix(FormatDuration(hoursToDuration(h)), " 0m")
}

func hoursToDuration(h float64) time.Duration {
	return time.Duration(h * float64(time.Hour))
}
