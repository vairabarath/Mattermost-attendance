package attendance

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Phase 5 jobs (spec §10): the 19:30 EOD reminder, the 10:00 task check in
// #attendance-admin, and the task/EOD parts of the daily and weekly reports.
// Task alerts only cover members with a linked GitHub login.

// TeamTaskSource reads a team's whole Project (implemented by internal/github).
type TeamTaskSource interface {
	Configured(teamID string) bool
	// ProjectTasks is every open issue on teamID's Project that isn't
	// Done, with Assignees set.
	ProjectTasks(ctx context.Context, teamID string) ([]Task, error)
	// BranchActive reports whether a branch of repo named `<number>-…`
	// has a commit since since.
	BranchActive(ctx context.Context, teamID, repo string, number int, since time.Time) (bool, error)
	// ClosedCounts is, per login, the issues assigned to them in teamID's
	// org that were closed in [from, to].
	ClosedCounts(ctx context.Context, teamID string, logins []string, from, to time.Time) (map[string]int, error)
}

// EODStatus is one submitted report, without its text.
type EODStatus struct {
	UserID, TeamID, Day string
	SyncStatus          string
	Done, Blocked       int
}

// EODAdminStore reads reports for admin views and jobs.
type EODAdminStore interface {
	// EODStatuses lists reports on days in [fromDay, toDay] for teamID
	// ("" = all teams).
	EODStatuses(ctx context.Context, teamID, fromDay, toDay string) ([]EODStatus, error)
	// EODThreadRoot is the root post of the team's EOD thread, or "".
	EODThreadRoot(ctx context.Context, teamID, day string) (string, error)
}

// Linker makes post permalinks.
type Linker interface {
	Permalink(ctx context.Context, teamID, postID string) (string, error)
}

const (
	staleWorkdays     = 3
	maxAlertPerMember = 10
)

// githubRetryDelays: the 10:00 task check retries GitHub before giving up
// for the day (job_runs lets it run only once).
var githubRetryDelays = []time.Duration{30 * time.Second, 2 * time.Minute}

// --- 19:30 EOD reminder ---

// EODReminders DMs members who checked out of a team today without sending
// that team's end-of-day report. One DM per person.
func (j *Jobs) EODReminders(ctx context.Context, now time.Time) {
	day := j.dayKey(now)
	j.once(ctx, "eod_reminder", day, func(ctx context.Context) error {
		holidays, err := j.Store.HolidaysOn(ctx, day)
		if err != nil {
			return err
		}
		settings, err := j.Store.AllMembers(ctx)
		if err != nil {
			return err
		}
		from, to := DayBounds(now, j.Loc)
		sessions, err := j.Store.AllSessionsOverlapping(ctx, from, to)
		if err != nil {
			return err
		}
		reports, err := j.Store.EODStatuses(ctx, "", day, day)
		if err != nil {
			return err
		}
		sent := map[string]bool{}
		for _, r := range reports {
			sent[r.UserID+"/"+r.TeamID] = true
		}
		configured := map[string]bool{}
		for _, t := range j.Teams {
			configured[t.ID] = IsWorkday(now.In(j.Loc), t.ID, j.Workdays, holidays)
		}

		n := 0
		for uid, ss := range sessions {
			if ms, ok := settings[uid]; ok && !ms.Tracked || j.Leave.OnLeave(ctx, uid, now) {
				continue
			}
			due := map[string]bool{}
			open := map[string]bool{}
			for _, s := range ss {
				if s.CheckIn.In(j.Loc).Format(DayFormat) != day || !configured[s.TeamID] {
					continue // the report is for the day the session started
				}
				if s.CheckOut == nil {
					open[s.TeamID] = true // the form opens when they check out
				} else if !sent[uid+"/"+s.TeamID] {
					due[s.TeamID] = true
				}
			}
			var names []string
			for team := range due {
				if !open[team] {
					names = append(names, "**"+j.TeamNames.TeamName(team)+"**")
				}
			}
			if len(names) == 0 {
				continue
			}
			sort.Strings(names)
			msg := fmt.Sprintf("📝 You haven't sent today's end-of-day report for %s yet. Run `/att eod submit` in that team when you have a minute.",
				strings.Join(names, ", "))
			if err := j.Msg.DirectMessage(ctx, uid, msg); err != nil {
				j.Log.Warn("eod reminder DM failed", "user_id", uid, "err", err)
				continue
			}
			n++
		}
		j.Log.Info("eod reminders sent", "count", n)
		return nil
	})
}

// --- 10:00 task check ---

// TaskAlerts posts, per team with a GitHub Project, overdue and stale
// issues of linked members (and, on Mondays, linked members with no open
// task) to #attendance-admin. Nothing is posted when all is well.
func (j *Jobs) TaskAlerts(ctx context.Context, now time.Time) {
	if j.GitHub == nil {
		return
	}
	day := j.dayKey(now)
	holidays, err := j.Store.HolidaysBetween(ctx, now.In(j.Loc).AddDate(0, 0, -31).Format(DayFormat), day)
	if err != nil {
		j.Log.Error("task alerts: holidays", "err", err)
		return
	}
	var rosters map[string][]RosterMember
	var settings map[string]MemberSettings
	for _, t := range j.Teams {
		t := t
		if t.AdminChannelID == "" || !j.GitHub.Configured(t.ID) || !IsWorkday(now.In(j.Loc), t.ID, j.Workdays, holidaysOn(holidays, day)) {
			continue
		}
		if rosters == nil {
			if rosters, settings, err = j.rosters(ctx); err != nil {
				j.Log.Error("task alerts: rosters", "err", err)
				return
			}
		}
		j.once(ctx, "task_alerts", day+"/"+t.Key, func(ctx context.Context) error {
			linked := linkedMembers(rosters[t.ID], settings)
			if len(linked) == 0 {
				return nil
			}
			var tasks []Task
			err := retryGitHub(ctx, func() (err error) {
				tasks, err = j.GitHub.ProjectTasks(ctx, t.ID)
				return err
			})
			if err != nil {
				return fmt.Errorf("project tasks: %w", err)
			}
			cutoff := staleCutoff(now.In(j.Loc), t.ID, j.Workdays, holidays)
			in := TaskAlertInput{
				TeamName: t.Name, Now: now, Loc: j.Loc, Tasks: tasks, Linked: linked,
				StaleSince: cutoff, NoTasks: now.In(j.Loc).Weekday() == time.Monday,
				BranchActive: func(task Task) bool {
					active, err := j.GitHub.BranchActive(ctx, t.ID, task.Repo, task.Number, cutoff)
					if err != nil { // unknown: don't call it stale
						j.Log.Warn("task alerts: branch check failed", "issue", task.URL, "err", err)
						return true
					}
					return active
				},
			}
			msg := BuildTaskAlert(in)
			if msg == "" {
				return nil
			}
			return j.Msg.CreatePost(ctx, t.AdminChannelID, msg)
		})
	}
}

func retryGitHub(ctx context.Context, fn func() error) error {
	err := fn()
	for _, d := range githubRetryDelays {
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(d):
		}
		err = fn()
	}
	return err
}

func holidaysOn(hs []Holiday, day string) []Holiday {
	var out []Holiday
	for _, h := range hs {
		if h.Day == day {
			out = append(out, h)
		}
	}
	return out
}

// linkedMembers maps lower(GitHub login) → username for tracked roster
// members with a linked login.
func linkedMembers(roster []RosterMember, settings map[string]MemberSettings) map[string]string {
	out := map[string]string{}
	for _, m := range roster {
		if ms, ok := settings[m.UserID]; ok && m.Tracked && ms.GitHubLogin != "" {
			out[strings.ToLower(ms.GitHubLogin)] = m.Username
		}
	}
	return out
}

// staleCutoff is the start of the day staleWorkdays working days before
// today: an In-progress issue with no activity since then is stale.
func staleCutoff(today time.Time, teamID string, workdays map[time.Weekday]bool, holidays []Holiday) time.Time {
	d, _ := DayBounds(today, today.Location())
	for n := 0; n < staleWorkdays; {
		d = d.AddDate(0, 0, -1)
		if IsWorkday(d, teamID, workdays, holidaysOn(holidays, d.Format(DayFormat))) {
			n++
		}
		if today.Sub(d) > 60*24*time.Hour { // no workdays configured
			break
		}
	}
	return d
}

// TaskAlertInput is what the 10:00 task check needs for one team.
type TaskAlertInput struct {
	TeamName     string
	Now          time.Time
	Loc          *time.Location
	Tasks        []Task            // open, not Done, on the Project
	Linked       map[string]string // lower(login) → username
	StaleSince   time.Time
	NoTasks      bool // include members with no open task (Mondays)
	BranchActive func(Task) bool
}

// BuildTaskAlert renders the task check, or "" if there's nothing to say.
func BuildTaskAlert(in TaskAlertInput) string {
	today := in.Now.In(in.Loc).Format(DayFormat)
	overdue := map[string][]Task{}
	stale := map[string][]Task{}
	hasTask := map[string]bool{}
	nOverdue, nStale := 0, 0
	SortTasks(in.Tasks, today)
	for _, t := range in.Tasks {
		var who []string
		for _, a := range t.Assignees {
			if u, ok := in.Linked[strings.ToLower(a)]; ok {
				who = append(who, u)
				hasTask[u] = true
			}
		}
		if len(who) == 0 {
			continue
		}
		if t.Overdue(today) {
			nOverdue++
			for _, u := range who {
				overdue[u] = append(overdue[u], t)
			}
		}
		if t.Status == StatusInProgress && t.Updated.Before(in.StaleSince) && !in.BranchActive(t) {
			nStale++
			for _, u := range who {
				stale[u] = append(stale[u], t)
			}
		}
	}
	var idle []string
	if in.NoTasks {
		for _, u := range in.Linked {
			if !hasTask[u] {
				idle = append(idle, "@"+u)
			}
		}
		sort.Strings(idle)
	}
	if nOverdue == 0 && nStale == 0 && len(idle) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "#### 🗂️ Task check — %s — %s\n", in.TeamName, in.Now.In(in.Loc).Format("Mon 2 Jan"))
	if nOverdue > 0 {
		fmt.Fprintf(&b, "\n**⚠️ Overdue (%d)**\n", nOverdue)
		writeTaskGroups(&b, overdue, func(t Task) string {
			return " — due " + fmtDue(t.Due, in.Loc)
		})
	}
	if nStale > 0 {
		fmt.Fprintf(&b, "\n**🐢 Stale (%d)** — In progress, no comments, updates or branch commits since %s\n",
			nStale, in.StaleSince.In(in.Loc).Format("Mon 2 Jan"))
		writeTaskGroups(&b, stale, func(Task) string { return "" })
	}
	if len(idle) > 0 {
		fmt.Fprintf(&b, "\n**😴 No open tasks:** %s\n", strings.Join(idle, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeTaskGroups(b *strings.Builder, groups map[string][]Task, suffix func(Task) string) {
	names := make([]string, 0, len(groups))
	for u := range groups {
		names = append(names, u)
	}
	sort.Strings(names)
	for _, u := range names {
		var parts []string
		for i, t := range groups[u] {
			if i == maxAlertPerMember {
				parts = append(parts, fmt.Sprintf("and %d more", len(groups[u])-i))
				break
			}
			parts = append(parts, fmt.Sprintf("[#%d %s](%s)%s", t.Number, safeText(mdEscape(clip(t.Title, 60))), t.URL, suffix(t)))
		}
		fmt.Fprintf(b, "- @%s: %s\n", u, strings.Join(parts, " · "))
	}
}

func fmtDue(due string, loc *time.Location) string {
	if d, err := time.ParseInLocation(DayFormat, due, loc); err == nil {
		return d.Format("2 Jan")
	}
	return due
}

// --- weekly report: tasks and EOD ---

// WeeklyTaskInput is the task/EOD part of a team's weekly report.
type WeeklyTaskInput struct {
	Rows      []TeamReportRow   // from BuildTeamReport
	Reports   map[string]int    // user → EOD reports sent in this team in the period
	Logins    map[string]string // user → GitHub login (linked members)
	Closed    map[string]int    // lower(login) → issues completed
	Overdue   map[string]int    // lower(login) → overdue now
	GitHubErr bool              // GitHub couldn't be read
}

// RenderWeeklyTasks is the second table of the weekly report.
func RenderWeeklyTasks(in WeeklyTaskInput) string {
	var b strings.Builder
	b.WriteString("\n\n**Tasks and end-of-day reports**\n\n| Member | EOD reports | Issues completed | Overdue now |\n|---|---|---|---|\n")
	rows := 0
	for _, r := range in.Rows {
		login, linked := in.Logins[r.UserID]
		if r.Days == 0 && !linked {
			continue
		}
		rows++
		eod := "—"
		if r.Days > 0 {
			eod = fmt.Sprintf("%d/%d", in.Reports[r.UserID], r.Days)
		}
		closed, over := "—", "—"
		if linked && !in.GitHubErr {
			closed = fmt.Sprint(in.Closed[strings.ToLower(login)])
			over = fmt.Sprint(in.Overdue[strings.ToLower(login)])
			if in.Overdue[strings.ToLower(login)] > 0 {
				over += " ⚠️"
			}
		}
		fmt.Fprintf(&b, "| @%s | %s | %s | %s |\n", r.Username, eod, closed, over)
	}
	if rows == 0 {
		return ""
	}
	note := "_EOD reports = reports sent ÷ days worked in this team. Issue columns only for members with a linked GitHub login (\"—\" otherwise)._"
	if in.GitHubErr {
		note = "_EOD reports = reports sent ÷ days worked in this team. ⚠️ GitHub couldn't be read, so the issue columns are empty this week._"
	}
	b.WriteString("\n" + note)
	return b.String()
}

// weeklyTasks gathers the weekly task/EOD table for team.
func (j *Jobs) weeklyTasks(ctx context.Context, teamID string, p Period, rows []TeamReportRow) string {
	statuses, err := j.Store.EODStatuses(ctx, teamID, p.FirstDay.Format(DayFormat), p.LastDay.Format(DayFormat))
	if err != nil {
		j.Log.Error("weekly report: eod statuses", "err", err)
		return ""
	}
	in := WeeklyTaskInput{Rows: rows, Reports: map[string]int{}, Logins: map[string]string{}}
	for _, s := range statuses {
		in.Reports[s.UserID]++
	}
	settings, err := j.Store.AllMembers(ctx)
	if err != nil {
		j.Log.Error("weekly report: members", "err", err)
		return ""
	}
	var logins []string
	if j.GitHub != nil && j.GitHub.Configured(teamID) {
		for _, r := range rows {
			if l := settings[r.UserID].GitHubLogin; l != "" {
				in.Logins[r.UserID] = l
				logins = append(logins, l)
			}
		}
	}
	if len(logins) > 0 {
		from, _ := DayBounds(p.FirstDay, j.Loc)
		_, to := DayBounds(p.LastDay, j.Loc)
		closed, err := j.GitHub.ClosedCounts(ctx, teamID, logins, from, to.Add(-time.Second))
		var tasks []Task
		if err == nil {
			tasks, err = j.GitHub.ProjectTasks(ctx, teamID)
		}
		if err != nil {
			j.Log.Warn("weekly report: github failed", "team_id", teamID, "err", err)
			in.GitHubErr = true
		} else {
			in.Closed, in.Overdue = map[string]int{}, map[string]int{}
			for l, n := range closed {
				in.Closed[strings.ToLower(l)] = n
			}
			today := j.Now().In(j.Loc).Format(DayFormat)
			for _, t := range tasks {
				if t.Overdue(today) {
					for _, a := range t.Assignees {
						in.Overdue[strings.ToLower(a)]++
					}
				}
			}
		}
	}
	return RenderWeeklyTasks(in)
}

// dailyEOD loads the EOD columns and thread link for the daily summary.
func (j *Jobs) dailyEOD(ctx context.Context, teamID, day string) (map[string]EODStatus, string) {
	statuses, err := j.Store.EODStatuses(ctx, teamID, day, day)
	if err != nil {
		j.Log.Warn("daily summary: eod statuses", "err", err)
		return nil, ""
	}
	out := map[string]EODStatus{}
	for _, s := range statuses {
		out[s.UserID] = s
	}
	link := ""
	if j.Links != nil {
		if root, err := j.Store.EODThreadRoot(ctx, teamID, day); err == nil && root != "" {
			if l, err := j.Links.Permalink(ctx, teamID, root); err == nil {
				link = l
			}
		}
	}
	return out, link
}
