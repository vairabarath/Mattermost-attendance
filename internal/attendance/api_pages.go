package attendance

import (
	"context"
	"sort"
	"strconv"
	"time"
)

// Panel pages added in phase 6 (ui-plan.md): the Home tile summary of the
// end-of-day report, Hours, Work from home and My settings. Each reuses the
// code behind the matching /att command.

// EODBrief is the end-of-day report's state for the Home tile.
type EODBrief struct {
	Available bool   `json:"available"` // there's a report to write (worked today / yesterday before 11:00)
	Day       string `json:"day,omitempty"`
	DayLabel  string `json:"day_label,omitempty"`
	Submitted bool   `json:"submitted"`
	SyncLine  string `json:"sync_line,omitempty"`
}

func (s *Service) eodBrief(ctx context.Context, userID, teamID string) (EODBrief, error) {
	day, ok, err := s.eodDay(ctx, userID, teamID)
	if err != nil || !ok {
		return EODBrief{}, err
	}
	b := EODBrief{Available: true, Day: day, DayLabel: s.dayStart(day).Format("Mon 2 Jan")}
	r, err := s.Store.GetEODReport(ctx, userID, teamID, day)
	if err != nil {
		return EODBrief{}, err
	}
	if r != nil {
		b.Submitted, b.SyncLine = true, syncLine(*r)
	}
	return b, nil
}

// --- Hours ---

// HoursDay is one day's work in this team.
type HoursDay struct {
	Day     string   `json:"day"`
	Label   string   `json:"label"`
	Seconds int      `json:"seconds"`
	Modes   []string `json:"modes"` // office, wfh
	Open    bool     `json:"open"`  // a session is still running
}

// HoursOrg is the time in one organization.
type HoursOrg struct {
	Team    TeamRef `json:"team"`
	Seconds int     `json:"seconds"`
}

// HoursState is the Hours page for one range.
type HoursState struct {
	Range       string     `json:"range"` // today, week, month
	Label       string     `json:"label"` // e.g. "Mon 24 Nov – Fri 28 Nov"
	Days        []HoursDay `json:"days"`
	TeamSeconds int        `json:"team_seconds"`
	AllSeconds  int        `json:"all_seconds"`
	ByOrg       []HoursOrg `json:"by_org"`
	// Expected hours, as in the reports: per day × workdays (no holidays)
	// from when the member was first seen up to yesterday; compared with
	// all organizations. None for untracked members or a range with no
	// finished day yet.
	HasExpected     bool   `json:"has_expected"`
	ExpectedSeconds int    `json:"expected_seconds"`
	ExpectedUntil   string `json:"expected_until,omitempty"`
	// WorkedUntilYesterday is the all-orgs time in the same days as the
	// expected hours (so the two compare fairly).
	WorkedUntilYesterday int     `json:"worked_until_yesterday"`
	PerDayHours          float64 `json:"per_day_hours"`
}

// PanelHours is the user's hours in range (today, week or month).
func (s *Service) PanelHours(ctx context.Context, userID, teamID, rng string) (HoursState, error) {
	if err := s.checkMember(ctx, userID, teamID); err != nil {
		return HoursState{}, err
	}
	now := s.Now()
	p, ok := PeriodFor(rng, now, s.Loc)
	if !ok {
		return HoursState{}, ErrBadRange
	}
	from, to := s.periodBounds(p)
	sessions, err := s.Store.SessionsOverlapping(ctx, userID, from, to)
	if err != nil {
		return HoursState{}, err
	}
	totals := inPeriod(NetByDay(sessions, now, s.Loc), p)
	st := HoursState{Range: rng, Days: []HoursDay{}, ByOrg: []HoursOrg{}}
	st.Label = p.FirstDay.Format("Mon 2 Jan")
	if !p.LastDay.Equal(p.FirstDay) {
		st.Label += " – " + p.LastDay.Format("Mon 2 Jan")
	}

	rows := map[string]UserDayRow{}
	for _, r := range BuildUserReport(sessions, teamID, p, now, s.Loc) {
		rows[r.Day] = r
	}
	today := now.In(s.Loc).Format(DayFormat)
	for d := p.FirstDay; !d.After(p.LastDay); d = d.AddDate(0, 0, 1) {
		day := d.Format(DayFormat)
		if day > today {
			break
		}
		r, worked := rows[day]
		// A week lists every day so far; a month only days with time.
		if !worked && rng == "month" {
			continue
		}
		hd := HoursDay{Day: day, Label: d.Format("Mon 2"), Seconds: int(r.Net.Seconds()), Open: r.Open, Modes: []string{}}
		for _, m := range []string{"office", "wfh"} {
			if r.Modes[m] {
				hd.Modes = append(hd.Modes, m)
			}
		}
		st.Days = append(st.Days, hd)
	}

	for _, tl := range sortedTeams(byTeam(totals), s.Teams) {
		st.ByOrg = append(st.ByOrg, HoursOrg{Seconds: int(tl.d.Seconds()), Team: TeamRef{Name: tl.name}})
	}
	for k, v := range totals {
		st.AllSeconds += int(v.Seconds())
		if k.TeamID == teamID {
			st.TeamSeconds += int(v.Seconds())
		}
	}
	// Fill the organizations' ids (sortedTeams gives names).
	ids := map[string]string{}
	for k := range totals {
		ids[s.Teams.TeamName(k.TeamID)] = k.TeamID
	}
	for i := range st.ByOrg {
		st.ByOrg[i].Team.ID = ids[st.ByOrg[i].Team.Name]
	}

	ms, err := s.Store.GetMember(ctx, userID)
	if err != nil {
		return HoursState{}, err
	}
	perDay, since, tracked := 8.0, time.Time{}, true
	if ms != nil {
		perDay, since, tracked = ms.ExpectedHours, ms.Since, ms.Tracked
	}
	st.PerDayHours = perDay
	if ep, ok := ExpectedPeriod(p, since, now, s.Loc); ok && tracked {
		holidays, err := s.Store.HolidaysBetween(ctx, ep.FirstDay.Format(DayFormat), ep.LastDay.Format(DayFormat))
		if err != nil {
			return HoursState{}, err
		}
		exp := ExpectedHours(perDay, ep, teamID, s.Workdays, holidays)
		st.HasExpected, st.ExpectedSeconds = true, int(exp.Seconds())
		st.ExpectedUntil = ep.LastDay.Format("Mon 2 Jan")
		first, last := ep.FirstDay.Format(DayFormat), ep.LastDay.Format(DayFormat)
		for k, v := range totals {
			if k.Day >= first && k.Day <= last {
				st.WorkedUntilYesterday += int(v.Seconds())
			}
		}
	}
	sort.SliceStable(st.ByOrg, func(i, j int) bool { return st.ByOrg[i].Seconds > st.ByOrg[j].Seconds })
	return st, nil
}

// --- Work from home ---

// WFHItem is one of the member's requests.
type WFHItem struct {
	ID        int64  `json:"id"`
	Dates     string `json:"dates"`
	Start     string `json:"start"`
	End       string `json:"end"`
	HalfDay   bool   `json:"half_day"`
	Reason    string `json:"reason"`
	Status    string `json:"status"` // pending, approved, rejected, cancelled
	DecidedBy string `json:"decided_by,omitempty"`
	Note      string `json:"note,omitempty"`
	CanCancel bool   `json:"can_cancel"`
}

// WFHState is the Work from home page.
type WFHState struct {
	Team     TeamRef   `json:"team"`
	Today    string    `json:"today"` // earliest start date
	Pending  int       `json:"pending"`
	Requests []WFHItem `json:"requests"`
}

// PanelWFH lists the user's WFH requests in teamID (newest first).
func (s *Service) PanelWFH(ctx context.Context, userID, teamID string) (WFHState, error) {
	if err := s.checkMember(ctx, userID, teamID); err != nil {
		return WFHState{}, err
	}
	rs, err := s.Store.ListWFH(ctx, userID, teamID, 15)
	if err != nil {
		return WFHState{}, err
	}
	today := s.Now().In(s.Loc).Format(DayFormat)
	st := WFHState{Team: TeamRef{ID: teamID, Name: s.Teams.TeamName(teamID)}, Today: today, Requests: []WFHItem{}}
	for _, r := range rs {
		it := WFHItem{ID: r.ID, Dates: s.wfhDates(r), Start: r.Start, End: r.End, HalfDay: r.HalfDay,
			Reason: r.Reason, Status: r.Status, DecidedBy: r.DecidedByName, Note: r.Note}
		// Same rule as /att wfh cancel: pending, or approved and not started.
		it.CanCancel = r.Status == WFHPending || (r.Status == WFHApproved && r.Start > today)
		if r.Status == WFHPending {
			st.Pending++
		}
		st.Requests = append(st.Requests, it)
	}
	return st, nil
}

// PanelRequestWFH sends a WFH request, exactly like the /att wfh form.
func (s *Service) PanelRequestWFH(ctx context.Context, userID, teamID, start, end string, halfDay bool, reason string) (DialogResult, error) {
	if err := s.checkMember(ctx, userID, teamID); err != nil {
		return DialogResult{}, err
	}
	return s.submitWFH(ctx, DialogSubmit{
		Fields: map[string]string{"k": dlgWFH, "u": userID, "t": teamID}, UserID: userID,
		Submission: map[string]any{"start_date": start, "end_date": end, "half_day": halfDay, "reason": reason},
	})
}

// PanelCancelWFH cancels one of the user's requests, like /att wfh cancel.
func (s *Service) PanelCancelWFH(ctx context.Context, userID, teamID string, id int64) (string, error) {
	if err := s.checkMember(ctx, userID, teamID); err != nil {
		return "", err
	}
	r, err := s.wfhCancel(ctx, Command{TeamID: teamID, UserID: userID}, strconv.FormatInt(id, 10))
	if err != nil {
		return "", err
	}
	return r.Text, nil
}

// --- My settings ---

// SettingsState is the My settings page (view only: admins change these).
type SettingsState struct {
	Team          TeamRef `json:"team"`
	IsAdmin       bool    `json:"is_admin"`
	GitHubSetUp   bool    `json:"github_set_up"`
	GitHubOrg     string  `json:"github_org,omitempty"`
	GitHubLogin   string  `json:"github_login,omitempty"`
	ExpectedHours float64 `json:"expected_hours"`
	Tracked       bool    `json:"tracked"`
	RemindersOn   bool    `json:"reminders_on"`
}

// PanelSettings is the user's settings as seen from teamID.
func (s *Service) PanelSettings(ctx context.Context, userID, teamID string) (SettingsState, error) {
	if err := s.checkMember(ctx, userID, teamID); err != nil {
		return SettingsState{}, err
	}
	st := SettingsState{Team: TeamRef{ID: teamID, Name: s.Teams.TeamName(teamID)}, IsAdmin: s.isAdmin(ctx, teamID, userID),
		ExpectedHours: 8, Tracked: true, RemindersOn: true}
	if s.Tasks != nil && s.Tasks.Configured(teamID) {
		st.GitHubSetUp, st.GitHubOrg = true, s.Tasks.OrgName(teamID)
	}
	ms, err := s.Store.GetMember(ctx, userID)
	if err != nil {
		return SettingsState{}, err
	}
	if ms != nil {
		st.GitHubLogin, st.ExpectedHours, st.Tracked, st.RemindersOn = ms.GitHubLogin, ms.ExpectedHours, ms.Tracked, ms.RemindersOn
	}
	return st, nil
}
