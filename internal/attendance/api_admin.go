package attendance

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The panel's admin pages (ui-plan.md phase 7): the team's day, one
// member's report and tasks, WFH requests to decide, and the week / month
// report with the CSV export. Admin rights are looked up in Mattermost on
// every call (the same check as the /att admin commands), and every call is
// limited to the team it names.

// ErrNotAdmin refuses admin calls from members.
var ErrNotAdmin = errors.New("not an admin of this team")

// idPattern is a Mattermost id (26 lowercase alphanumerics).
var idPattern = regexp.MustCompile(`^[a-z0-9]{26}$`)

// checkAdmin is checkMember plus team-admin rights.
func (s *Service) checkAdmin(ctx context.Context, userID, teamID string) error {
	if err := s.checkMember(ctx, userID, teamID); err != nil {
		return err
	}
	if !s.isAdmin(ctx, teamID, userID) {
		return ErrNotAdmin
	}
	return nil
}

// --- Team today ---

// TeamMemberDay is one member's line on the team's day.
type TeamMemberDay struct {
	UserID      string     `json:"user_id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	State       string     `json:"state"` // working, break, out, absent, elsewhere
	Elsewhere   string     `json:"elsewhere,omitempty"`
	Since       *time.Time `json:"since,omitempty"` // open session's check-in
	Mode        string     `json:"mode,omitempty"`
	Seconds     int        `json:"seconds"` // net in this team on the day
	EOD         string     `json:"eod"`     // sent, missing, waiting (still checked in), none (didn't work)
	Done        int        `json:"done"`
	Blocked     int        `json:"blocked"`
	Sync        string     `json:"sync,omitempty"`
	Tracked     bool       `json:"tracked"`
}

// TeamDay is the Team page's day view.
type TeamDay struct {
	Team       TeamRef         `json:"team"`
	Day        string          `json:"day"`
	DayLabel   string          `json:"day_label"`
	Members    []TeamMemberDay `json:"members"`
	Working    int             `json:"working"`
	OnBreak    int             `json:"on_break"`
	Out        int             `json:"out"`
	Absent     int             `json:"absent"`
	EODSent    int             `json:"eod_sent"`
	EODDue     int             `json:"eod_due"` // worked and checked out
	WFHPending int             `json:"wfh_pending"`
}

var stateOrder = map[string]int{"working": 0, "break": 1, "elsewhere": 2, "out": 3, "absent": 4}

// AdminTeamDay is teamID's day: every member of the team (from Mattermost),
// with live status, hours, the end-of-day report and its GitHub sync.
func (s *Service) AdminTeamDay(ctx context.Context, adminID, teamID, day string) (TeamDay, error) {
	if err := s.checkAdmin(ctx, adminID, teamID); err != nil {
		return TeamDay{}, err
	}
	now := s.Now()
	if day == "" {
		day = now.In(s.Loc).Format(DayFormat)
	}
	d, err := time.ParseInLocation(DayFormat, day, s.Loc)
	if err != nil {
		return TeamDay{}, err
	}
	rosters, settings, err := loadRosters(ctx, s.Roster, s.Store, []string{teamID})
	if err != nil {
		return TeamDay{}, err
	}
	from, to := DayBounds(d, s.Loc)
	sessions, err := s.Store.AllSessionsOverlapping(ctx, from, to)
	if err != nil {
		return TeamDay{}, err
	}
	statuses, err := s.Store.EODStatuses(ctx, teamID, day, day)
	if err != nil {
		return TeamDay{}, err
	}
	pending, err := s.Store.PendingWFH(ctx, teamID, "")
	if err != nil {
		return TeamDay{}, err
	}
	reports := map[string]EODStatus{}
	for _, st := range statuses {
		reports[st.UserID] = st
	}

	out := TeamDay{Team: TeamRef{ID: teamID, Name: s.Teams.TeamName(teamID)}, Day: day,
		DayLabel: d.Format("Mon 2 Jan"), Members: []TeamMemberDay{}, WFHPending: len(pending)}
	seen := map[string]bool{}
	add := func(uid, username string, tracked bool) {
		if seen[uid] {
			return
		}
		seen[uid] = true
		ms := settings[uid]
		m := TeamMemberDay{UserID: uid, Username: username, DisplayName: ms.DisplayName, Tracked: tracked, State: "absent", EOD: "none"}
		if m.Username == "" {
			m.Username = ms.Username
		}
		worked := false
		for _, ss := range sessions[uid] {
			if !overlapsDay(ss, day, now, s.Loc) {
				continue
			}
			if ss.TeamID != teamID {
				if ss.CheckOut == nil && m.State == "absent" {
					m.State, m.Elsewhere = "elsewhere", s.Teams.TeamName(ss.TeamID)
				}
				continue
			}
			worked = true
			if ss.CheckOut == nil {
				ci := ss.CheckIn
				m.Since, m.Mode = &ci, ss.WorkMode
				m.State = "working"
				if onBreak(&ss) {
					m.State = "break"
				}
			} else if m.State == "absent" || m.State == "elsewhere" {
				m.State = "out"
			}
		}
		m.Seconds = int(TeamTotalsForDay(NetByDay(sessions[uid], now, s.Loc), day)[teamID].Seconds())
		if r, ok := reports[uid]; ok {
			m.EOD, m.Done, m.Blocked, m.Sync = "sent", r.Done, r.Blocked, r.SyncStatus
		} else if worked && m.Since != nil {
			m.EOD = "waiting"
		} else if worked {
			m.EOD = "missing"
		}
		if m.State == "absent" && !tracked && !worked {
			return // untracked and not around: not part of the day
		}
		out.Members = append(out.Members, m)
	}
	for _, rm := range rosters[teamID] {
		add(rm.UserID, rm.Username, rm.Tracked)
	}
	// Anyone who worked here or sent a report but isn't on the roster any more.
	for uid := range sessions {
		for _, ss := range sessions[uid] {
			if ss.TeamID == teamID && overlapsDay(ss, day, now, s.Loc) {
				add(uid, "", false)
			}
		}
	}
	for uid := range reports {
		add(uid, "", false)
	}

	for _, m := range out.Members {
		switch m.State {
		case "working":
			out.Working++
		case "break":
			out.OnBreak++
		case "out":
			out.Out++
		case "absent", "elsewhere":
			out.Absent++
		}
		switch m.EOD {
		case "sent":
			out.EODSent++
			out.EODDue++
		case "missing":
			out.EODDue++
		}
	}
	sort.SliceStable(out.Members, func(i, j int) bool {
		a, b := out.Members[i], out.Members[j]
		if stateOrder[a.State] != stateOrder[b.State] {
			return stateOrder[a.State] < stateOrder[b.State]
		}
		return strings.ToLower(a.Username) < strings.ToLower(b.Username)
	})
	return out, nil
}

// --- One member ---

// MemberDetail is one member's report and tasks for an admin.
type MemberDetail struct {
	UserID      string      `json:"user_id"`
	Username    string      `json:"username"`
	Day         string      `json:"day"`
	DayLabel    string      `json:"day_label"`
	Worked      bool        `json:"worked"`
	Report      string      `json:"report,omitempty"` // Mattermost markdown, as posted in the thread
	SubmittedAt *time.Time  `json:"submitted_at,omitempty"`
	SyncLine    string      `json:"sync_line,omitempty"`
	Tasks       *TasksState `json:"tasks,omitempty"`
}

// AdminMember is userID's end-of-day report on day and their open tasks.
func (s *Service) AdminMember(ctx context.Context, adminID, teamID, userID, day string) (MemberDetail, error) {
	if err := s.checkAdmin(ctx, adminID, teamID); err != nil {
		return MemberDetail{}, err
	}
	if !idPattern.MatchString(userID) {
		return MemberDetail{}, ErrNotTeamMember
	}
	tm, err := s.People.GetTeamMember(ctx, teamID, userID)
	if err != nil {
		return MemberDetail{}, err
	}
	if tm == nil || tm.DeleteAt != 0 {
		return MemberDetail{}, ErrNotTeamMember
	}
	if day == "" {
		day = s.Now().In(s.Loc).Format(DayFormat)
	}
	md := MemberDetail{UserID: userID, Day: day, DayLabel: s.dayStart(day).Format("Mon 2 Jan")}
	ms, err := s.Store.GetMember(ctx, userID)
	if err != nil {
		return MemberDetail{}, err
	}
	if ms != nil {
		md.Username = ms.Username
	}
	_, _, md.Worked, err = s.dayWork(ctx, userID, teamID, day)
	if err != nil {
		return MemberDetail{}, err
	}
	rep, err := s.Store.GetEODReport(ctx, userID, teamID, day)
	if err != nil {
		return MemberDetail{}, err
	}
	if rep != nil {
		if md.Report, err = s.renderEODForPost(ctx, *rep); err != nil {
			return MemberDetail{}, err
		}
		at := rep.UpdatedAt
		md.SubmittedAt, md.SyncLine = &at, syncLine(*rep)
	}
	if s.Tasks != nil && s.Tasks.Configured(teamID) {
		ts := TasksState{Configured: true, Tasks: []TaskItem{}}
		if ms != nil && ms.GitHubLogin != "" {
			ts.Linked = true
			gctx, cancel := context.WithTimeout(ctx, githubTimeout)
			tasks, err := s.Tasks.AssignedTasks(gctx, teamID, ms.GitHubLogin)
			cancel()
			if err != nil {
				ts.Error = "Couldn't load tasks from GitHub right now."
			} else {
				today := s.Now().In(s.Loc).Format(DayFormat)
				SortTasks(tasks, today)
				picked := s.pickedIssues(ctx, userID, teamID, day)
				for _, t := range tasks {
					ts.Tasks = append(ts.Tasks, TaskItem{Number: t.Number, Title: t.Title, URL: t.URL, Repo: t.Repo,
						Status: t.Status, Priority: t.Priority, Due: t.Due, Overdue: t.Overdue(today), OffBoard: t.OffBoard, Picked: picked[t.URL]})
				}
			}
		}
		md.Tasks = &ts
	}
	return md, nil
}

// --- WFH requests ---

// AdminWFHItem is a team member's WFH request.
type AdminWFHItem struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Dates     string `json:"dates"`
	HalfDay   bool   `json:"half_day"`
	Reason    string `json:"reason"`
	Status    string `json:"status"`
	DecidedBy string `json:"decided_by,omitempty"`
	Note      string `json:"note,omitempty"`
	Urgent    bool   `json:"urgent"` // for today, or tomorrow and asked after 20:00
	UsedMonth string `json:"used_month,omitempty"`
}

// AdminWFHState is the Team page's WFH tab.
type AdminWFHState struct {
	Pending []AdminWFHItem `json:"pending"`
	Recent  []AdminWFHItem `json:"recent"` // decided or cancelled, last 30 days
	Today   []string       `json:"today"`  // on approved WFH today
}

func (s *Service) adminWFHItem(ctx context.Context, r WFHRequest, now time.Time) AdminWFHItem {
	it := AdminWFHItem{ID: r.ID, Username: r.Username, Dates: s.wfhDates(r), HalfDay: r.HalfDay, Reason: r.Reason,
		Status: r.Status, DecidedBy: r.DecidedByName, Note: r.Note}
	if r.Status == WFHPending {
		if st, err := time.ParseInLocation(DayFormat, r.Start, s.Loc); err == nil {
			it.Urgent = IsUrgent(st, now, s.Loc)
		}
		if used, err := s.usedThisMonth(ctx, r); err == nil {
			it.UsedMonth = fmt.Sprintf("%g", used)
		}
	}
	return it
}

// AdminWFH lists teamID's pending requests and the last 30 days.
func (s *Service) AdminWFH(ctx context.Context, adminID, teamID string) (AdminWFHState, error) {
	if err := s.checkAdmin(ctx, adminID, teamID); err != nil {
		return AdminWFHState{}, err
	}
	now := s.Now().In(s.Loc)
	rs, err := s.Store.ListTeamWFH(ctx, teamID, now.AddDate(0, 0, -wfhAllDays).Format(DayFormat), 100)
	if err != nil {
		return AdminWFHState{}, err
	}
	today := now.Format(DayFormat)
	st := AdminWFHState{Pending: []AdminWFHItem{}, Recent: []AdminWFHItem{}, Today: []string{}}
	for _, r := range rs {
		it := s.adminWFHItem(ctx, r, now)
		if r.Status == WFHPending {
			st.Pending = append(st.Pending, it)
		} else {
			st.Recent = append(st.Recent, it)
		}
		if r.Status == WFHApproved && r.Start <= today && today <= r.End {
			name := "@" + r.Username
			if r.HalfDay {
				name += " (½)"
			}
			st.Today = append(st.Today, name)
		}
	}
	return st, nil
}

// AdminDecideWFH approves or rejects a pending request, exactly like the
// buttons in #attendance-admin (audit, DM to the member, the admin post is
// updated). A rejection needs a reason.
func (s *Service) AdminDecideWFH(ctx context.Context, adminID, teamID string, id int64, approve bool, note string) (DialogResult, error) {
	if err := s.checkAdmin(ctx, adminID, teamID); err != nil {
		return DialogResult{}, err
	}
	r, err := s.Store.GetWFH(ctx, id)
	if err != nil {
		return DialogResult{}, err
	}
	if r == nil || r.TeamID != teamID {
		return DialogResult{Error: "Request not found."}, nil
	}
	if r.Status != WFHPending {
		return DialogResult{Error: fmt.Sprintf("Already %s.", r.Status)}, nil
	}
	status := WFHApproved
	note = truncate(strings.TrimSpace(note), maxNoteLen)
	if !approve {
		status = WFHRejected
		if len([]rune(note)) < 3 {
			return DialogResult{Errors: map[string]string{"reason": "Please give a reason (it's sent to the member)."}}, nil
		}
	}
	decided, err := s.decide(ctx, id, teamID, status, adminID, note)
	if errors.Is(err, ErrAlreadyDecided) {
		return DialogResult{Error: fmt.Sprintf("Already %s by @%s.", decided.Status, decided.DecidedByName)}, nil
	}
	if err != nil {
		return DialogResult{}, err
	}
	s.refreshAdminPost(ctx, *decided)
	return DialogResult{}, nil
}

// --- Reports ---

// TeamReportItem is one member's row in the week / month report.
type TeamReportItem struct {
	Username    string `json:"username"`
	Days        int    `json:"days"`
	TeamSeconds int    `json:"team_seconds"`
	AllSeconds  int    `json:"all_seconds"`
	Expected    int    `json:"expected_seconds"`
	HasExpected bool   `json:"has_expected"`
	Short       bool   `json:"short"` // all orgs under 90% of expected
	WFHDays     int    `json:"wfh_days"`
	AutoClosed  int    `json:"auto_closed"`
	EODSent     int    `json:"eod_sent"`
	EODDue      int    `json:"eod_due"` // days worked here and checked out
}

// TeamReport is the Team page's week / month report.
type TeamReport struct {
	Range   string           `json:"range"`
	Label   string           `json:"label"`
	Rows    []TeamReportItem `json:"rows"`
	EODSent int              `json:"eod_sent"`
	EODDue  int              `json:"eod_due"`
}

// eodDue is, per user, the days in the period with time in teamID on which a
// report was due: every such day, except today while still checked in.
func eodDue(in TeamReportInput) map[string]map[string]bool {
	today := in.Now.In(in.Loc).Format(DayFormat)
	out := map[string]map[string]bool{}
	for uid, sessions := range in.Sessions {
		days := map[string]bool{}
		for k, v := range inPeriod(NetByDay(sessions, in.Now, in.Loc), in.Period) {
			if k.TeamID == in.TeamID && v > 0 {
				days[k.Day] = true
			}
		}
		for _, s := range sessions {
			if s.TeamID == in.TeamID && s.CheckOut == nil {
				delete(days, today)
			}
		}
		if len(days) > 0 {
			out[uid] = days
		}
	}
	return out
}

// AdminReport is /att report team for this week or month.
func (s *Service) AdminReport(ctx context.Context, adminID, teamID, rng string) (TeamReport, error) {
	if err := s.checkAdmin(ctx, adminID, teamID); err != nil {
		return TeamReport{}, err
	}
	if rng != "week" && rng != "month" {
		return TeamReport{}, ErrBadRange
	}
	now := s.Now()
	p, _ := PeriodFor(rng, now, s.Loc)
	in, err := s.teamReportInput(ctx, teamID, p, now)
	if err != nil {
		return TeamReport{}, err
	}
	statuses, err := s.Store.EODStatuses(ctx, teamID, p.FirstDay.Format(DayFormat), p.LastDay.Format(DayFormat))
	if err != nil {
		return TeamReport{}, err
	}
	due := eodDue(in)
	sent := map[string]int{}
	for _, st := range statuses {
		if due[st.UserID][st.Day] {
			sent[st.UserID]++
		}
	}
	out := TeamReport{Range: rng, Label: p.FirstDay.Format("Mon 2 Jan") + " – " + p.LastDay.Format("Mon 2 Jan"), Rows: []TeamReportItem{}}
	for _, r := range BuildTeamReport(in) {
		item := TeamReportItem{Username: r.Username, Days: r.Days, TeamSeconds: int(r.Team.Seconds()),
			AllSeconds: int(r.AllOrgs.Seconds()), Expected: int(r.Expected.Seconds()), HasExpected: r.HasExpected,
			Short: r.Short(), WFHDays: r.WFHDays, AutoClosed: r.AutoClosed,
			EODSent: sent[r.UserID], EODDue: len(due[r.UserID])}
		out.EODSent += item.EODSent
		out.EODDue += item.EODDue
		out.Rows = append(out.Rows, item)
	}
	return out, nil
}

// AdminExport sends this month's (or month's, YYYY-MM) CSV to the admin by
// DM, like /att export, and returns the message.
func (s *Service) AdminExport(ctx context.Context, adminID, teamID, month string) (string, error) {
	if err := s.checkAdmin(ctx, adminID, teamID); err != nil {
		return "", err
	}
	r, err := s.export(ctx, Command{TeamID: teamID, UserID: adminID}, month)
	if err != nil {
		return "", err
	}
	return r.Text, nil
}
