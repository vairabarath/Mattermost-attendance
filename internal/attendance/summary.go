package attendance

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// RosterMember is a counted member of a team: not a bot, not deactivated.
type RosterMember struct {
	UserID   string
	Username string
	Tracked  bool
}

// FilterRoster drops bots and deactivated users and attaches the tracked
// flag (members never seen before default to tracked).
func FilterRoster(users []mattermost.User, settings map[string]MemberSettings) []RosterMember {
	var out []RosterMember
	for _, u := range users {
		if u.IsBot || u.DeleteAt != 0 {
			continue
		}
		tracked := true
		if ms, ok := settings[u.ID]; ok {
			tracked = ms.Tracked
		}
		out = append(out, RosterMember{UserID: u.ID, Username: u.Username, Tracked: tracked})
	}
	return out
}

// SummaryInput is everything the daily summary for one team needs.
type SummaryInput struct {
	TeamID    string
	TeamName  string
	Teams     Teams
	Day       time.Time // any instant on the local day
	Now       time.Time
	Loc       *time.Location
	Roster    []RosterMember       // this team's filtered members
	Usernames map[string]string    // user id → username, for people not in Roster
	Sessions  map[string][]Session // per user: all sessions overlapping the day, all teams
	OnLeave   func(userID string) bool
	// EOD (user → this team's report for the day) adds the EOD columns;
	// nil leaves them out. EODThread links the day's EOD thread.
	EOD       map[string]EODStatus
	EODThread string
}

// BuildDailySummary renders the 20:00 summary for one team (spec section 9).
func BuildDailySummary(in SummaryInput) string {
	day := in.Day.In(in.Loc).Format(DayFormat)
	name := func(id string) string {
		if u := in.Usernames[id]; u != "" {
			return "@" + u
		}
		for _, r := range in.Roster {
			if r.UserID == id {
				return "@" + r.Username
			}
		}
		return "unknown user"
	}

	type row struct {
		uid   string
		who   string
		net   time.Duration
		modes map[string]bool
		open  *time.Time
	}
	var worked []row
	workedOther := map[string][]string{} // user → other org names
	anyToday := map[string]bool{}

	for uid, sessions := range in.Sessions {
		perTeam := TeamTotalsForDay(NetByDay(sessions, in.Now, in.Loc), day)
		r := row{uid: uid, who: name(uid), modes: map[string]bool{}}
		inThisTeam := false
		for _, s := range sessions {
			if !overlapsDay(s, day, in.Now, in.Loc) {
				continue
			}
			anyToday[uid] = true
			if s.TeamID != in.TeamID {
				continue
			}
			inThisTeam = true
			r.modes[s.WorkMode] = true
			if s.CheckOut == nil {
				ci := s.CheckIn
				r.open = &ci
			}
		}
		if inThisTeam {
			r.net = perTeam[in.TeamID]
			worked = append(worked, r)
		} else if anyToday[uid] {
			for teamID := range perTeam {
				workedOther[uid] = append(workedOther[uid], in.Teams.TeamName(teamID))
			}
			if len(workedOther[uid]) == 0 { // e.g. zero-length session
				for _, s := range sessions {
					workedOther[uid] = append(workedOther[uid], in.Teams.TeamName(s.TeamID))
				}
			}
		}
	}
	sort.Slice(worked, func(i, j int) bool { return worked[i].who < worked[j].who })

	var b strings.Builder
	fmt.Fprintf(&b, "#### 📋 Daily attendance — %s — %s\n", in.TeamName, in.Day.In(in.Loc).Format("Mon 2 Jan"))

	wfh := 0
	for _, r := range worked {
		if r.modes["wfh"] {
			wfh++
		}
	}
	if len(worked) == 0 {
		b.WriteString("Nobody checked in to this team today.\n")
	} else {
		fmt.Fprintf(&b, "**Worked today: %d** · 🏠 WFH: %d\n\n", len(worked), wfh)
		if in.EOD == nil {
			b.WriteString("| Member | Hours | Mode |\n|---|---|---|\n")
		} else {
			b.WriteString("| Member | Hours | Mode | EOD | Done | Blocked |\n|---|---|---|---|---|---|\n")
		}
		for _, r := range worked {
			fmt.Fprintf(&b, "| %s | %s | %s |", r.who, FormatDuration(r.net), modesIcons(r.modes))
			if in.EOD != nil {
				if rep, ok := in.EOD[r.uid]; ok {
					fmt.Fprintf(&b, " ✅ | %d | %s |", rep.Done, blockedCell(rep.Blocked))
				} else if r.open != nil {
					b.WriteString(" ⏳ | | |") // still working: the form opens at check-out
				} else {
					b.WriteString(" ❌ | | |")
				}
			}
			b.WriteString("\n")
		}
		if in.EODThread != "" {
			fmt.Fprintf(&b, "\n📝 [Today's end-of-day reports](%s)\n", in.EODThread)
		}
	}

	var still []string
	for _, r := range worked {
		if r.open != nil {
			still = append(still, fmt.Sprintf("%s (since %s)", r.who, fmtLocalClock(*r.open, in.Now, in.Loc)))
		}
	}
	if len(still) > 0 {
		fmt.Fprintf(&b, "\n**Still checked in:** %s\n", strings.Join(still, ", "))
	}

	var other, absent []string
	for _, m := range in.Roster {
		if orgs, ok := workedOther[m.UserID]; ok {
			sort.Strings(orgs)
			other = append(other, fmt.Sprintf("@%s (%s)", m.Username, strings.Join(orgs, ", ")))
			continue
		}
		if anyToday[m.UserID] || !m.Tracked {
			continue
		}
		if in.OnLeave != nil && in.OnLeave(m.UserID) {
			continue
		}
		absent = append(absent, "@"+m.Username)
	}
	sort.Strings(other)
	sort.Strings(absent)
	if len(other) > 0 {
		fmt.Fprintf(&b, "\n**Worked for another org today:** %s\n", strings.Join(other, ", "))
	}
	if len(absent) > 0 {
		fmt.Fprintf(&b, "\n**Not checked in:** %s\n", strings.Join(absent, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// overlapsDay reports whether the session touches the local day.
func overlapsDay(s Session, day string, now time.Time, loc *time.Location) bool {
	d, err := time.ParseInLocation(DayFormat, day, loc)
	if err != nil {
		return false
	}
	start, end := DayBounds(d, loc)
	out := now
	if s.CheckOut != nil {
		out = *s.CheckOut
	}
	return s.CheckIn.Before(end) && !out.Before(start)
}

func modesIcons(m map[string]bool) string {
	var parts []string
	if m["office"] {
		parts = append(parts, "🏢")
	}
	if m["wfh"] {
		parts = append(parts, "🏠")
	}
	return strings.Join(parts, " ")
}

func fmtLocalClock(t, now time.Time, loc *time.Location) string {
	if t.In(loc).Format(DayFormat) == now.In(loc).Format(DayFormat) {
		return t.In(loc).Format("3:04 PM")
	}
	return t.In(loc).Format("2 Jan, 3:04 PM")
}

func blockedCell(n int) string {
	if n == 0 {
		return "0"
	}
	return fmt.Sprintf("%d 🚧", n)
}
