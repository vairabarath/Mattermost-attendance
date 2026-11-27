package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

// Adding issues to the board. An issue is added to team T's Project when it
// is an open issue in a non-archived repo of T's org, assigned to a linked
// member, and not on the Project yet — found either when a member looks
// (/att in, /att tasks, the EOD form, a pick) or by the periodic sweep.
// Unassigned issues, closed issues and PRs are never added.

var _ attendance.BoardSweeper = (*Tasks)(nil)

// adoptable reports whether issues may be added to teamID's Project: the
// feature is on and teamID's org maps to that one Project only (two teams
// sharing an org with different Projects would make the target ambiguous).
func (t *Tasks) adoptable(teamID string) bool {
	tp, ok := t.teams[teamID]
	if !ok || !t.AddAssigned {
		return false
	}
	for id, other := range t.teams {
		if id != teamID && strings.EqualFold(other.Org, tp.Org) && other.Number != tp.Number {
			return false
		}
	}
	return true
}

const addItemMutation = `mutation($p: ID!, $c: ID!) {
  addProjectV2ItemById(input: {projectId: $p, contentId: $c}) {
    item { id status: fieldValueByName(name: "Status") { ... on ProjectV2ItemFieldSingleSelectValue { name } } }
  }
}`

// addToBoard adds the issue (contentID) to teamID's Project and, if its
// Status is empty, sets it to Todo. Adding an issue that's already there
// returns the existing item, so this is safe to repeat.
func (t *Tasks) addToBoard(ctx context.Context, teamID string, schema *Schema, contentID string) (string, error) {
	var out struct {
		AddProjectV2ItemByID struct {
			Item struct {
				ID     string                 `json:"id"`
				Status *struct{ Name string } `json:"status"`
			} `json:"item"`
		} `json:"addProjectV2ItemById"`
	}
	if err := t.app.GraphQL(ctx, t.teams[teamID].Org, addItemMutation,
		map[string]any{"p": schema.ProjectID, "c": contentID}, &out, true); err != nil {
		return "", fmt.Errorf("add to project: %w", err)
	}
	item := out.AddProjectV2ItemByID.Item
	if item.Status == nil || item.Status.Name == "" {
		if todo, ok := schema.Status.Options[attendance.StatusTodo]; ok {
			if err := t.setStatus(ctx, teamID, schema, item.ID, todo); err != nil {
				return item.ID, err
			}
		}
	}
	return item.ID, nil
}

// AddToBoard implements attendance.TaskSource: it adds the OffBoard tasks
// (from AssignedTasks / EODTasks, i.e. assigned to the member in the
// team's org) to the team's Project.
func (t *Tasks) AddToBoard(ctx context.Context, teamID string, tasks []attendance.Task) (int, error) {
	if !t.adoptable(teamID) {
		return 0, nil
	}
	schema, err := t.Schema(ctx, teamID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, task := range tasks {
		if !task.OffBoard || task.NodeID == "" {
			continue
		}
		if _, err := t.addToBoard(ctx, teamID, schema, task.NodeID); err != nil {
			return n, err
		}
		n++
		t.log.Info("issue added to board", "team_id", teamID, "issue", task.URL, "by", "member")
	}
	return n, nil
}

// maxSweepPages bounds one sweep (100 issues a page; search stops at 1000).
const maxSweepPages = 10

const sweepQuery = `query($q: String!, $after: String) {
  search(type: ISSUE, query: $q, first: 100, after: $after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on Issue {
        id
        number
        url
        state
        assignees(first: 10) { nodes { login } }
        projectItems(first: 20) { nodes { project { id } } }
      }
    }
  }
}`

type sweepPage struct {
	Search struct {
		IssueCount int `json:"issueCount"`
		PageInfo   struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []struct {
			ID        string `json:"id"`
			Number    int    `json:"number"`
			URL       string `json:"url"`
			State     string `json:"state"`
			Assignees struct {
				Nodes []struct {
					Login string `json:"login"`
				} `json:"nodes"`
			} `json:"assignees"`
			ProjectItems struct {
				Nodes []struct {
					Project struct {
						ID string `json:"id"`
					} `json:"project"`
				} `json:"nodes"`
			} `json:"projectItems"`
		} `json:"nodes"`
	} `json:"search"`
}

// SweepBoard implements attendance.BoardSweeper: it adds every open issue
// in teamID's org that's assigned to a linked login (lowercase keys) and
// not on the Project. A zero since looks at all open issues; otherwise
// only issues updated since then (creating or assigning an issue updates it).
func (t *Tasks) SweepBoard(ctx context.Context, teamID string, since time.Time, linked map[string]bool) (int, error) {
	tp, ok := t.teams[teamID]
	if !ok || !t.adoptable(teamID) || len(linked) == 0 {
		return 0, nil
	}
	schema, err := t.Schema(ctx, teamID)
	if err != nil {
		return 0, err
	}
	q := fmt.Sprintf("org:%s is:issue is:open archived:false", tp.Org)
	if !since.IsZero() {
		q += " updated:>=" + since.UTC().Format("2006-01-02T15:04:05+00:00")
	}
	added := 0
	vars := map[string]any{"q": q, "after": nil}
	for page := 0; page < maxSweepPages; page++ {
		var p sweepPage
		if err := t.app.GraphQL(ctx, tp.Org, sweepQuery, vars, &p, false); err != nil {
			return added, err
		}
		for _, n := range p.Search.Nodes {
			if n.Number == 0 || n.ID == "" || n.State != "OPEN" {
				continue
			}
			onBoard := false
			for _, it := range n.ProjectItems.Nodes {
				if it.Project.ID == schema.ProjectID {
					onBoard = true
				}
			}
			mine := false
			for _, a := range n.Assignees.Nodes {
				if linked[strings.ToLower(a.Login)] {
					mine = true
				}
			}
			if onBoard || !mine {
				continue
			}
			if _, err := t.addToBoard(ctx, teamID, schema, n.ID); err != nil {
				return added, err
			}
			added++
			t.log.Info("issue added to board", "team_id", teamID, "issue", n.URL, "by", "sweep")
		}
		if !p.Search.PageInfo.HasNextPage {
			return added, nil
		}
		vars["after"] = p.Search.PageInfo.EndCursor
	}
	t.log.Warn("github: board sweep truncated", "team_id", teamID, "pages", maxSweepPages)
	return added, nil
}
