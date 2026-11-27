package attendance

import (
	"context"
	"errors"
)

// TaskItem is one task in the panel's list.
type TaskItem struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Repo     string `json:"repo"`
	Status   string `json:"status"`
	Priority string `json:"priority,omitempty"`
	Due      string `json:"due,omitempty"` // YYYY-MM-DD
	Overdue  bool   `json:"overdue"`
	OffBoard bool   `json:"off_board"` // just being added to the board
	Picked   bool   `json:"picked"`    // picked today
}

// TasksState is the panel's task list. Exactly one of the states applies:
// GitHub not set up for the team, the user not linked, GitHub failing
// (Error), or the list (possibly empty).
type TasksState struct {
	Configured bool       `json:"configured"`
	Linked     bool       `json:"linked"`
	Error      string     `json:"error,omitempty"`
	Tasks      []TaskItem `json:"tasks"`
}

// PanelTasks lists the user's open tasks on teamID's Project (the same list
// as /att tasks), sorted, with today's picks marked. Issues not on the
// board yet are listed and added in the background.
func (s *Service) PanelTasks(ctx context.Context, userID, teamID string) (TasksState, error) {
	if err := s.checkMember(ctx, userID, teamID); err != nil {
		return TasksState{}, err
	}
	st := TasksState{Tasks: []TaskItem{}}
	if s.Tasks == nil || !s.Tasks.Configured(teamID) {
		return st, nil
	}
	st.Configured = true
	ms, err := s.Store.GetMember(ctx, userID)
	if err != nil {
		return TasksState{}, err
	}
	if ms == nil || ms.GitHubLogin == "" {
		return st, nil
	}
	st.Linked = true

	gctx, cancel := context.WithTimeout(ctx, githubTimeout)
	tasks, err := s.Tasks.AssignedTasks(gctx, teamID, ms.GitHubLogin)
	cancel()
	switch {
	case errors.Is(err, ErrGitHubNotSetUp):
		s.Log.Error("github project misconfigured", "team_id", teamID, "err", err)
		st.Error = "The GitHub Project for this team isn't set up as the bot needs. Ask an admin."
		return st, nil
	case err != nil:
		s.Log.Warn("panel: github tasks failed", "team_id", teamID, "user_id", userID, "err", err)
		st.Error = "Couldn't load your tasks from GitHub right now."
		return st, nil
	}
	s.addToBoardLater(teamID, tasks)

	today := s.Now().In(s.Loc).Format(DayFormat)
	SortTasks(tasks, today)
	picked := s.pickedIssues(ctx, userID, teamID, s.pickDay(ctx, userID, teamID))
	for _, t := range tasks {
		st.Tasks = append(st.Tasks, TaskItem{
			Number: t.Number, Title: t.Title, URL: t.URL, Repo: t.Repo, Status: t.Status,
			Priority: t.Priority, Due: t.Due, Overdue: t.Overdue(today), OffBoard: t.OffBoard,
			Picked: picked[t.URL],
		})
	}
	return st, nil
}

// PanelPick picks a task for today, exactly like the pick menu in chat, and
// returns its message (Mattermost markdown).
func (s *Service) PanelPick(ctx context.Context, userID, teamID, issueURL string) (string, error) {
	if err := s.checkMember(ctx, userID, teamID); err != nil {
		return "", err
	}
	r, err := s.pickClick(ctx, Click{
		Action: ActPick, UserID: userID, Selected: issueURL,
		Fields: map[string]string{"a": ActPick, "u": userID, "t": teamID},
	})
	if err != nil {
		return "", err
	}
	return r.Text, nil
}
