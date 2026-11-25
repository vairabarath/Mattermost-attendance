package attendance

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Period is a reporting range of whole local days, ending today.
type Period struct {
	Name     string // "today", "this week", "this month"
	FirstDay time.Time
	LastDay  time.Time // inclusive
}

// PeriodFor returns the period for "today", "week" (Mon–today) or
// "month" (1st–today) in loc. ok is false for unknown names.
func PeriodFor(name string, now time.Time, loc *time.Location) (Period, bool) {
	local := now.In(loc)
	y, m, d := local.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, loc)
	switch name {
	case "", "today":
		return Period{"today", today, today}, true
	case "week":
		// Weekday(): Sunday=0. Days since Monday = (wd+6)%7.
		back := (int(today.Weekday()) + 6) % 7
		return Period{"this week", today.AddDate(0, 0, -back), today}, true
	case "month":
		return Period{"this month", time.Date(y, m, 1, 0, 0, 0, 0, loc), today}, true
	}
	return Period{}, false
}

// Days lists the period's days as YYYY-MM-DD.
func (p Period) Days() []string {
	var out []string
	for d := p.FirstDay; !d.After(p.LastDay); d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format(DayFormat))
	}
	return out
}

func (s *Service) hours(ctx context.Context, userID, arg string) (Reply, error) {
	now := s.Now()
	p, ok := PeriodFor(strings.ToLower(strings.TrimSpace(arg)), now, s.Loc)
	if !ok {
		return text("Usage: `/att hours [today|week|month]`"), nil
	}
	totals, err := s.netForPeriod(ctx, userID, p, now)
	if err != nil {
		return Reply{}, err
	}
	open, err := s.Store.OpenSession(ctx, userID)
	if err != nil {
		return Reply{}, err
	}

	var b strings.Builder
	if p.Name == "today" {
		b.WriteString(s.renderToday(totals, now))
	} else {
		b.WriteString(s.renderDays(p, totals))
	}
	if open != nil {
		state := "🟢 Currently checked in"
		if onBreak(open) {
			state = "☕ Currently on break, checked in"
		}
		fmt.Fprintf(&b, "\n\n%s to **%s** since %s.", state, s.Teams.TeamName(open.TeamID), s.fmtSince(open.CheckIn, now))
	}
	return text(b.String()), nil
}

// netForPeriod runs the single hours calculation over the period's days.
func (s *Service) netForPeriod(ctx context.Context, userID string, p Period, now time.Time) (map[DayKey]time.Duration, error) {
	from, _ := DayBounds(p.FirstDay, s.Loc)
	_, to := DayBounds(p.LastDay, s.Loc)
	sessions, err := s.Store.SessionsOverlapping(ctx, userID, from, to)
	if err != nil {
		return nil, err
	}
	all := NetByDay(sessions, now, s.Loc)
	first, last := p.FirstDay.Format(DayFormat), p.LastDay.Format(DayFormat)
	for k := range all {
		if k.Day < first || k.Day > last { // YYYY-MM-DD sorts as dates
			delete(all, k)
		}
	}
	return all, nil
}

// teamTotals returns net time per team over [fromDay, toDay].
func (s *Service) teamTotals(ctx context.Context, userID string, fromDay, toDay time.Time) (map[string]time.Duration, error) {
	p := Period{FirstDay: fromDay.In(s.Loc), LastDay: toDay.In(s.Loc)}
	totals, err := s.netForPeriod(ctx, userID, p, s.Now())
	if err != nil {
		return nil, err
	}
	return byTeam(totals), nil
}

func (s *Service) renderToday(totals map[DayKey]time.Duration, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#### Your hours today (%s)\n", now.In(s.Loc).Format("Mon 2 Jan"))
	perTeam := byTeam(totals)
	if len(perTeam) == 0 {
		b.WriteString("No time recorded today.")
		return b.String()
	}
	b.WriteString("| Organization | Hours |\n|---|---|\n")
	for _, tl := range sortedTeams(perTeam, s.Teams) {
		fmt.Fprintf(&b, "| %s | %s |\n", tl.name, FormatDuration(tl.d))
	}
	fmt.Fprintf(&b, "| **Total** | **%s** |", FormatDuration(sum(perTeam)))
	return b.String()
}

func (s *Service) renderDays(p Period, totals map[DayKey]time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#### Your hours %s (%s – %s)\n", p.Name, p.FirstDay.Format("Mon 2 Jan"), p.LastDay.Format("Mon 2 Jan"))
	if len(totals) == 0 {
		b.WriteString("No time recorded.")
		return b.String()
	}
	b.WriteString("| Day | Hours | By organization |\n|---|---|---|\n")
	for _, day := range p.Days() {
		perTeam := map[string]time.Duration{}
		for k, v := range totals {
			if k.Day == day {
				perTeam[k.TeamID] += v
			}
		}
		if len(perTeam) == 0 {
			continue // only days with recorded time
		}
		parts := make([]string, 0, len(perTeam))
		for _, tl := range sortedTeams(perTeam, s.Teams) {
			parts = append(parts, tl.name+" "+FormatDuration(tl.d))
		}
		t, _ := time.ParseInLocation(DayFormat, day, s.Loc)
		fmt.Fprintf(&b, "| %s | %s | %s |\n", t.Format("Mon 2 Jan"), FormatDuration(sum(perTeam)), strings.Join(parts, ", "))
	}
	perTeam := byTeam(totals)
	fmt.Fprintf(&b, "| **Total** | **%s** | |\n\n**By organization:**", FormatDuration(sum(perTeam)))
	for _, tl := range sortedTeams(perTeam, s.Teams) {
		fmt.Fprintf(&b, "\n- %s: %s", tl.name, FormatDuration(tl.d))
	}
	return b.String()
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

func byTeam(totals map[DayKey]time.Duration) map[string]time.Duration {
	out := map[string]time.Duration{}
	for k, v := range totals {
		out[k.TeamID] += v
	}
	return out
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
