package attendance

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// Start of day: at check-in (and with /att tasks) members see their tasks
// and pick what they're working on today. A pick moves the issue to In
// progress and gives it a status dropdown in that day's EOD form.

const (
	ActPick = "pick" // personal select menu: pick today's task

	// checkInTaskTimeout bounds the GitHub call at check-in: checking in
	// never waits long on GitHub.
	checkInTaskTimeout = 3 * time.Second
	pickTimeout        = 6 * time.Second
	maxPickOptions     = 25
)

// ErrIssueClosed refuses picking an issue that is already closed.
var ErrIssueClosed = errors.New("issue is closed")

// Pick is the outcome of picking an issue.
type Pick struct {
	Number  int
	Title   string
	Status  string // Project Status after the pick
	Started bool   // the pick moved it to In progress
}

// PickStore records the issues members picked per day.
type PickStore interface {
	// AddTaskPick is idempotent.
	AddTaskPick(ctx context.Context, userID, teamID, day, issueURL string) error
	TaskPicks(ctx context.Context, userID, teamID, day string) ([]string, error)
}

// startOfDayTasks adds the member's tasks and the pick menu to the check-in
// reply. Any GitHub problem leaves the check-in as it is, with a note.
func (s *Service) startOfDayTasks(ctx context.Context, r Reply, userID, teamID string) Reply {
	if s.Tasks == nil || !s.Tasks.Configured(teamID) {
		return r
	}
	ms, err := s.Store.GetMember(ctx, userID)
	if err != nil || ms == nil || ms.GitHubLogin == "" {
		return r
	}
	gctx, cancel := context.WithTimeout(ctx, checkInTaskTimeout)
	tasks, err := s.Tasks.AssignedTasks(gctx, teamID, ms.GitHubLogin)
	cancel()
	if err != nil {
		s.Log.Warn("check-in: github tasks failed", "team_id", teamID, "user_id", userID, "err", err)
		r.Text += "\n\n_Couldn't load your tasks right now — try `/att tasks`._"
		return r
	}
	r.Text += "\n\n" + s.taskListText(tasks, "Your tasks", teamID)
	r.Attachments = append(r.Attachments, s.pickMenu(userID, teamID, tasks)...)
	return r
}

// pickMenu is the "Pick today's task" select for tasks (already sorted).
func (s *Service) pickMenu(userID, teamID string, tasks []Task) []mattermost.Attachment {
	if s.Signer == nil || s.ActionURL == "" || len(tasks) == 0 {
		return nil
	}
	var opts []mattermost.ActionOption
	for _, t := range tasks {
		if len(opts) == maxPickOptions {
			break
		}
		label := fmt.Sprintf("#%d %s", t.Number, t.Title)
		if t.Status != "" {
			label = clip(label, 60) + " (" + t.Status + ")"
		}
		opts = append(opts, mattermost.ActionOption{Text: clip(label, 80), Value: t.URL})
	}
	return []mattermost.Attachment{{
		Text: "Working on something today? Pick it to move it to **In progress** and add it to your end-of-day report.",
		Actions: []mattermost.Action{{
			ID: "attpick", Name: "Pick today's task", Type: "select", Options: opts,
			Integration: mattermost.Integration{
				URL:     s.ActionURL,
				Context: s.Signer.Sign(map[string]string{"a": ActPick, "u": userID, "t": teamID}),
			},
		}},
	}}
}

var pickURLRe = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9-]+)/[A-Za-z0-9._-]+/issues/[0-9]+$`)

// pickClick handles a choice in the pick menu. The chosen value isn't
// signed: it must be an issue URL in the team's org, and GitHub must show
// the issue assigned to the clicker.
func (s *Service) pickClick(ctx context.Context, c Click) (Reply, error) {
	teamID, issueURL := c.Fields["t"], strings.TrimSpace(c.Selected)
	reply := func(msg string) Reply { return Reply{Text: msg, EphemeralOnly: true} }
	if s.Tasks == nil || s.Writer == nil || !s.Tasks.Configured(teamID) {
		return reply("GitHub isn't set up for **" + s.Teams.TeamName(teamID) + "** yet."), nil
	}
	m := pickURLRe.FindStringSubmatch(issueURL)
	if m == nil || !strings.EqualFold(m[1], s.Tasks.OrgName(teamID)) {
		s.Log.Warn("pick: rejected value", "user_id", c.UserID, "value", clip(issueURL, 200))
		return reply("That isn't one of your tasks. Run `/att tasks` again."), nil
	}
	ms, err := s.Store.GetMember(ctx, c.UserID)
	if err != nil {
		return Reply{}, err
	}
	if ms == nil || ms.GitHubLogin == "" {
		return reply("Your GitHub login isn't linked yet. " + sendLoginToAdmin), nil
	}

	gctx, cancel := context.WithTimeout(ctx, pickTimeout)
	p, err := s.Writer.StartTask(gctx, teamID, ms.GitHubLogin, issueURL)
	cancel()
	ref := issueRef(issueURL)
	if p.Number > 0 {
		ref = fmt.Sprintf("#%d %s", p.Number, safeText(mdEscape(clip(p.Title, 80))))
	}
	switch {
	case errors.Is(err, ErrNotAssigned):
		return reply(fmt.Sprintf("**%s** isn't assigned to you on GitHub. Run `/att tasks` for your current list.", issueRef(issueURL))), nil
	case errors.Is(err, ErrIssueClosed):
		return reply(fmt.Sprintf("**%s** is already closed.", ref)), nil
	case err != nil:
		s.Log.Warn("pick: github failed", "team_id", teamID, "user_id", c.UserID, "issue", issueURL, "err", err)
		return reply("⚠️ Couldn't update GitHub right now. Please try again in a minute."), nil
	}

	if err := s.Store.AddTaskPick(ctx, c.UserID, teamID, s.pickDay(ctx, c.UserID, teamID), issueURL); err != nil {
		return Reply{}, err
	}
	if p.Started {
		return reply(fmt.Sprintf("▶️ **%s** is now **In progress**. It'll be in your end-of-day report.", ref)), nil
	}
	return reply(fmt.Sprintf("📌 **%s** is already **%s**, so it's left as is. It'll be in your end-of-day report.", ref, p.Status)), nil
}

// pickDay is the day a pick counts for: the day the member's open session
// in teamID started (the EOD report's day), else today.
func (s *Service) pickDay(ctx context.Context, userID, teamID string) string {
	if open, err := s.Store.OpenSession(ctx, userID); err == nil && open != nil && open.TeamID == teamID {
		return open.CheckIn.In(s.Loc).Format(DayFormat)
	}
	return s.Now().In(s.Loc).Format(DayFormat)
}

// pickedIssues is the set of issues picked on day (empty on error: the
// form still opens).
func (s *Service) pickedIssues(ctx context.Context, userID, teamID, day string) map[string]bool {
	urls, err := s.Store.TaskPicks(ctx, userID, teamID, day)
	if err != nil {
		s.Log.Warn("eod: loading picks failed", "user_id", userID, "err", err)
	}
	out := make(map[string]bool, len(urls))
	for _, u := range urls {
		out[u] = true
	}
	return out
}
