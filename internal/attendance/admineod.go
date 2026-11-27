package attendance

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// Admin views of tasks and end-of-day reports (spec §8):
// /att tasks @user, /att eod @user [day], /att eod team [day].

const eodAdminUsage = "Admins: `/att eod @user [YYYY-MM-DD]` — a member's report · `/att eod team [YYYY-MM-DD]` — everyone's, as a table."

// teamMember resolves @username to a member of teamID, or returns a reply
// explaining why it can't.
func (s *Service) teamMember(ctx context.Context, teamID, who string) (*mattermost.User, *Reply, error) {
	m := usernameRe.FindStringSubmatch(strings.ToLower(who))
	if m == nil {
		r := text(fmt.Sprintf("`%s` isn't a username.", who))
		return nil, &r, nil
	}
	u, err := s.People.GetUserByUsername(ctx, m[1])
	if err != nil {
		return nil, nil, err
	}
	if u == nil || u.IsBot {
		r := text(fmt.Sprintf("No member `@%s`.", m[1]))
		return nil, &r, nil
	}
	tm, err := s.People.GetTeamMember(ctx, teamID, u.ID)
	if err != nil {
		return nil, nil, err
	}
	if tm == nil {
		r := text(fmt.Sprintf("`@%s` isn't a member of **%s**.", u.Username, s.Teams.TeamName(teamID)))
		return nil, &r, nil
	}
	return u, nil, nil
}

// memberTasks is /att tasks @user (admins).
func (s *Service) memberTasks(ctx context.Context, c Command, who string) (Reply, error) {
	if r, err := s.requireAdmin(ctx, c.TeamID, c.UserID); r != nil || err != nil {
		return deref(r), err
	}
	u, r, err := s.teamMember(ctx, c.TeamID, who)
	if r != nil || err != nil {
		return deref(r), err
	}
	ms, err := s.Store.GetMember(ctx, u.ID)
	if err != nil {
		return Reply{}, err
	}
	if ms == nil || ms.GitHubLogin == "" {
		return text(fmt.Sprintf("@%s has no linked GitHub login. Link it with `/att member @%s github <username>`.", u.Username, u.Username)), nil
	}
	gctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	tasks, err := s.Tasks.AssignedTasks(gctx, c.TeamID, ms.GitHubLogin)
	if err != nil {
		s.Log.Warn("github tasks failed", "team_id", c.TeamID, "target", u.ID, "err", err)
		return text("⚠️ Couldn't load tasks from GitHub right now. Please try again in a minute."), nil
	}
	if len(tasks) == 0 {
		return text(fmt.Sprintf("@%s has no open tasks on the **%s** GitHub Project.", u.Username, s.Teams.TeamName(c.TeamID))), nil
	}
	s.addToBoardLater(c.TeamID, tasks)
	return text(s.taskListText(tasks, "@"+u.Username+"'s open tasks", c.TeamID)), nil
}

// eodAdmin handles /att eod @user|team [day].
func (s *Service) eodAdmin(ctx context.Context, c Command, fields []string) (Reply, error) {
	if r, err := s.requireAdmin(ctx, c.TeamID, c.UserID); r != nil || err != nil {
		return deref(r), err
	}
	if len(fields) > 2 {
		return text(eodAdminUsage), nil
	}
	day := s.Now().In(s.Loc).Format(DayFormat)
	if len(fields) == 2 {
		d, err := time.ParseInLocation(DayFormat, fields[1], s.Loc)
		if !dayRe.MatchString(fields[1]) || err != nil {
			return text(eodAdminUsage), nil
		}
		day = d.Format(DayFormat)
	}
	if strings.EqualFold(fields[0], "team") {
		return s.eodTeam(ctx, c.TeamID, day)
	}
	u, r, err := s.teamMember(ctx, c.TeamID, fields[0])
	if r != nil || err != nil {
		return deref(r), err
	}
	return s.eodMember(ctx, c.TeamID, u, day)
}

func (s *Service) eodMember(ctx context.Context, teamID string, u *mattermost.User, day string) (Reply, error) {
	when := s.dayStart(day).Format("Mon 2 Jan")
	rep, err := s.Store.GetEODReport(ctx, u.ID, teamID, day)
	if err != nil {
		return Reply{}, err
	}
	if rep == nil {
		_, _, worked, err := s.dayWork(ctx, u.ID, teamID, day)
		if err != nil {
			return Reply{}, err
		}
		if !worked {
			return text(fmt.Sprintf("@%s didn't work in **%s** on %s.", u.Username, s.Teams.TeamName(teamID), when)), nil
		}
		return text(fmt.Sprintf("❌ @%s worked in **%s** on %s but hasn't sent an end-of-day report.", u.Username, s.Teams.TeamName(teamID), when)), nil
	}
	msg, err := s.renderEODForPost(ctx, *rep)
	if err != nil {
		return Reply{}, err
	}
	foot := fmt.Sprintf("%s · Submitted %s", syncLine(*rep), rep.UpdatedAt.In(s.Loc).Format("2 Jan, 3:04 PM"))
	if link := s.postLink(ctx, teamID, rep.ReplyPostID); link != "" {
		foot += fmt.Sprintf(" · [open in thread](%s)", link)
	}
	return text(fmt.Sprintf("#### @%s's end-of-day report — %s, %s\n%s\n\n_%s_", u.Username, s.Teams.TeamName(teamID), when, msg, foot)), nil
}

func (s *Service) postLink(ctx context.Context, teamID, postID string) string {
	if postID == "" || s.Posts == nil {
		return ""
	}
	link, err := s.Posts.Permalink(ctx, teamID, postID)
	if err != nil {
		return ""
	}
	return link
}

// eodTeam is the team table for day: everyone who worked in the team or
// sent a report, with hours, EOD ✅/❌, Done and Blocked counts and sync.
func (s *Service) eodTeam(ctx context.Context, teamID, day string) (Reply, error) {
	d, err := time.ParseInLocation(DayFormat, day, s.Loc)
	if err != nil {
		return Reply{}, err
	}
	from, to := DayBounds(d, s.Loc)
	sessions, err := s.Store.AllSessionsOverlapping(ctx, from, to)
	if err != nil {
		return Reply{}, err
	}
	statuses, err := s.Store.EODStatuses(ctx, teamID, day, day)
	if err != nil {
		return Reply{}, err
	}
	settings, err := s.Store.AllMembers(ctx)
	if err != nil {
		return Reply{}, err
	}
	type row struct {
		name string
		net  time.Duration
		open bool
		rep  *EODStatus
	}
	rows := map[string]*row{}
	get := func(uid string) *row {
		if rows[uid] == nil {
			name := settings[uid].Username
			if name == "" {
				name = "unknown user"
			}
			rows[uid] = &row{name: name}
		}
		return rows[uid]
	}
	now := s.Now()
	for uid, ss := range sessions {
		net := TeamTotalsForDay(NetByDay(ss, now, s.Loc), day)[teamID]
		var mine []Session
		for _, x := range ss {
			if x.TeamID == teamID && overlapsDay(x, day, now, s.Loc) {
				mine = append(mine, x)
			}
		}
		if len(mine) == 0 {
			continue
		}
		r := get(uid)
		r.net = net
		for _, x := range mine {
			if x.CheckOut == nil {
				r.open = true
			}
		}
	}
	for i := range statuses {
		get(statuses[i].UserID).rep = &statuses[i]
	}

	when := d.Format("Mon 2 Jan")
	if len(rows) == 0 {
		return text(fmt.Sprintf("Nobody worked in **%s** on %s.", s.Teams.TeamName(teamID), when)), nil
	}
	list := make([]*row, 0, len(rows))
	for _, r := range rows {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })

	var b strings.Builder
	sent := 0
	for _, r := range list {
		eod, done, blocked, sync := "❌", "", "", ""
		switch {
		case r.rep != nil:
			sent++
			eod, done, blocked, sync = "✅", fmt.Sprint(r.rep.Done), blockedCell(r.rep.Blocked), syncCell(r.rep.SyncStatus)
		case r.open:
			eod = "⏳"
		}
		fmt.Fprintf(&b, "| @%s | %s | %s | %s | %s | %s |\n", r.name, FormatDuration(r.net), eod, done, blocked, sync)
	}
	head := fmt.Sprintf("#### 📝 End-of-day reports — %s — %s\n**Sent: %d of %d**\n\n| Member | Hours | EOD | Done | Blocked | GitHub |\n|---|---|---|---|---|---|\n",
		s.Teams.TeamName(teamID), when, sent, len(list))
	foot := "\n_⏳ = still checked in (the form opens at check-out). `/att eod @user` shows one report._"
	if root, err := s.Store.EODThreadRoot(ctx, teamID, day); err == nil {
		if link := s.postLink(ctx, teamID, root); link != "" {
			foot = fmt.Sprintf("\n📝 [All reports in the thread](%s)", link) + foot
		}
	}
	return text(head + b.String() + foot), nil
}

func syncCell(status string) string {
	switch status {
	case "synced":
		return "✅"
	case "failed":
		return "⚠️ failed"
	default:
		return "⏳"
	}
}
