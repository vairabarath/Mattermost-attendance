package attendance

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// shortThreshold: all-orgs hours below this share of expected get ⚠️.
const shortThreshold = 0.9

// inPeriod keeps only the days inside p.
func inPeriod(totals map[DayKey]time.Duration, p Period) map[DayKey]time.Duration {
	first, last := p.FirstDay.Format(DayFormat), p.LastDay.Format(DayFormat)
	out := map[DayKey]time.Duration{}
	for k, v := range totals {
		if k.Day >= first && k.Day <= last { // YYYY-MM-DD sorts as dates
			out[k] += v
		}
	}
	return out
}

// ExpectedHours is perDay × workdays in p for teamID (configured weekdays,
// minus that team's and all-org holidays).
func ExpectedHours(perDay float64, p Period, teamID string, workdays map[time.Weekday]bool, holidays []Holiday) time.Duration {
	n := 0
	for d := p.FirstDay; !d.After(p.LastDay); d = d.AddDate(0, 0, 1) {
		day := d.Format(DayFormat)
		var todays []Holiday
		for _, h := range holidays {
			if h.Day == day {
				todays = append(todays, h)
			}
		}
		if IsWorkday(d, teamID, workdays, todays) {
			n++
		}
	}
	return time.Duration(float64(n) * perDay * float64(time.Hour))
}

// TeamReportInput is everything a team report needs.
type TeamReportInput struct {
	TeamID    string
	Period    Period
	Now       time.Time
	Loc       *time.Location
	Roster    []RosterMember
	Usernames map[string]string
	Settings  map[string]MemberSettings
	Sessions  map[string][]Session // per user, all teams, overlapping the period
	Workdays  map[time.Weekday]bool
	Holidays  []Holiday // holidays inside the period
}

// TeamReportRow is one member's line in a team report.
type TeamReportRow struct {
	UserID, Username string
	Days             int // days with time in this team
	Team, AllOrgs    time.Duration
	Expected         time.Duration
	HasExpected      bool // false for untracked members
	WFHDays          int
	AutoClosed       int
}

// Short reports whether all-orgs hours are clearly under expected.
func (r TeamReportRow) Short() bool {
	return r.HasExpected && r.Expected > 0 && float64(r.AllOrgs) < shortThreshold*float64(r.Expected)
}

// BuildTeamReport computes one row per tracked team member, plus anyone
// else who logged time in this team during the period.
func BuildTeamReport(in TeamReportInput) []TeamReportRow {
	rows := map[string]*TeamReportRow{}
	for _, m := range in.Roster {
		if m.Tracked {
			rows[m.UserID] = &TeamReportRow{UserID: m.UserID, Username: m.Username, HasExpected: true}
		}
	}
	first, last := in.Period.FirstDay.Format(DayFormat), in.Period.LastDay.Format(DayFormat)

	for uid, sessions := range in.Sessions {
		totals := inPeriod(NetByDay(sessions, in.Now, in.Loc), in.Period)
		r := rows[uid]
		if r == nil {
			var team time.Duration
			for k, v := range totals {
				if k.TeamID == in.TeamID {
					team += v
				}
			}
			if team == 0 {
				continue // not a tracked member and no time here
			}
			r = &TeamReportRow{UserID: uid, Username: in.Usernames[uid]}
			rows[uid] = r
		}
		for k, v := range totals {
			r.AllOrgs += v
			if k.TeamID == in.TeamID && v > 0 {
				r.Team += v
				r.Days++
			}
		}
		wfh := map[string]bool{}
		for _, s := range sessions {
			if s.TeamID != in.TeamID {
				continue
			}
			day := s.CheckIn.In(in.Loc).Format(DayFormat)
			if s.AutoClosed && day >= first && day <= last {
				r.AutoClosed++
			}
			if s.WorkMode == "wfh" {
				for k := range inPeriod(NetByDay([]Session{s}, in.Now, in.Loc), in.Period) {
					wfh[k.Day] = true
				}
			}
		}
		r.WFHDays = len(wfh)
	}

	out := make([]TeamReportRow, 0, len(rows))
	for _, r := range rows {
		if r.HasExpected {
			perDay := 8.0
			if ms, ok := in.Settings[r.UserID]; ok {
				perDay = ms.ExpectedHours
			}
			r.Expected = ExpectedHours(perDay, in.Period, in.TeamID, in.Workdays, in.Holidays)
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// RenderTeamReport renders rows as a Mattermost markdown table.
func RenderTeamReport(title string, rows []TeamReportRow) string {
	var b strings.Builder
	b.WriteString(title + "\n")
	if len(rows) == 0 {
		b.WriteString("No tracked members and no recorded time.")
		return b.String()
	}
	b.WriteString("| Member | Days | This team | All orgs | Expected | WFH days | Auto-closed |\n|---|---|---|---|---|---|---|\n")
	var team, all time.Duration
	for _, r := range rows {
		name := "@" + r.Username
		if r.Short() {
			name += " ⚠️"
		}
		exp := "—"
		if r.HasExpected {
			exp = FormatDuration(r.Expected)
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %d | %d |\n",
			name, r.Days, FormatDuration(r.Team), FormatDuration(r.AllOrgs), exp, r.WFHDays, r.AutoClosed)
		team += r.Team
		all += r.AllOrgs
	}
	fmt.Fprintf(&b, "| **Total** | | **%s** | %s | | | |\n\n", FormatDuration(team), FormatDuration(all))
	b.WriteString("_Expected = expected hours/day × workdays (minus holidays), compared with **all orgs**. ⚠️ = under 90% of expected. \"—\" = not tracked._")
	return b.String()
}

// UserDayRow is one day of a member's report in one team.
type UserDayRow struct {
	Day        string
	Net        time.Duration
	Breaks     time.Duration
	Modes      map[string]bool
	AutoClosed bool
	Open       bool
}

// BuildUserReport returns per-day rows for the member's time in teamID.
// Break time per day = gross span − net, using the same day split.
func BuildUserReport(sessions []Session, teamID string, p Period, now time.Time, loc *time.Location) []UserDayRow {
	byDay := map[string]*UserDayRow{}
	get := func(day string) *UserDayRow {
		if byDay[day] == nil {
			byDay[day] = &UserDayRow{Day: day, Modes: map[string]bool{}}
		}
		return byDay[day]
	}
	for _, s := range sessions {
		if s.TeamID != teamID {
			continue
		}
		net := inPeriod(NetByDay([]Session{s}, now, loc), p)
		gross := s
		gross.Breaks = nil
		for k, g := range inPeriod(NetByDay([]Session{gross}, now, loc), p) {
			r := get(k.Day)
			r.Net += net[k]
			r.Breaks += g - net[k]
			r.Modes[s.WorkMode] = true
			if s.AutoClosed {
				r.AutoClosed = true
			}
			if s.CheckOut == nil {
				r.Open = true
			}
		}
	}
	out := make([]UserDayRow, 0, len(byDay))
	for _, r := range byDay {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}
