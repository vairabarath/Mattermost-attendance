package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

// Team-wide reads for the admin jobs (spec §10): the whole Project, branch
// activity for stale detection, and closed-issue counts per member.

var _ attendance.TeamTaskSource = (*Tasks)(nil)

// maxProjectPages bounds ProjectTasks (100 items a page).
const maxProjectPages = 10

const projectItemsQuery = `query($org: String!, $number: Int!, $after: String) {
  organization(login: $org) {
    projectV2(number: $number) {
      items(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          status: fieldValueByName(name: "Status") { ... on ProjectV2ItemFieldSingleSelectValue { name } }
          priority: fieldValueByName(name: "Priority") { ... on ProjectV2ItemFieldSingleSelectValue { name } }
          due: fieldValueByName(name: "Due date") { ... on ProjectV2ItemFieldDateValue { date } }
          content {
            ... on Issue {
              number
              title
              url
              state
              updatedAt
              repository { nameWithOwner }
              assignees(first: 10) { nodes { login } }
            }
          }
        }
      }
    }
  }
}`

type projectItemsPage struct {
	Organization struct {
		ProjectV2 *struct {
			Items struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					Status   *struct{ Name string } `json:"status"`
					Priority *struct{ Name string } `json:"priority"`
					Due      *struct{ Date string } `json:"due"`
					Content  struct {
						Number     int       `json:"number"`
						Title      string    `json:"title"`
						URL        string    `json:"url"`
						State      string    `json:"state"`
						UpdatedAt  time.Time `json:"updatedAt"`
						Repository struct {
							NameWithOwner string `json:"nameWithOwner"`
						} `json:"repository"`
						Assignees struct {
							Nodes []struct {
								Login string `json:"login"`
							} `json:"nodes"`
						} `json:"assignees"`
					} `json:"content"`
				} `json:"nodes"`
			} `json:"items"`
		} `json:"projectV2"`
	} `json:"organization"`
}

// ProjectTasks implements attendance.TeamTaskSource: every open issue on
// teamID's Project that isn't Done, with its assignees.
func (t *Tasks) ProjectTasks(ctx context.Context, teamID string) ([]attendance.Task, error) {
	tp, ok := t.teams[teamID]
	if !ok {
		return nil, attendance.ErrGitHubNotSetUp
	}
	if _, err := t.Schema(ctx, teamID); err != nil {
		return nil, err
	}
	var out []attendance.Task
	vars := map[string]any{"org": tp.Org, "number": tp.Number, "after": nil}
	for page := 0; page < maxProjectPages; page++ {
		var p projectItemsPage
		if err := t.app.GraphQL(ctx, tp.Org, projectItemsQuery, vars, &p, false); err != nil {
			return nil, err
		}
		if p.Organization.ProjectV2 == nil {
			return nil, attendance.ErrGitHubNotSetUp
		}
		items := p.Organization.ProjectV2.Items
		for _, n := range items.Nodes {
			c := n.Content
			if c.Number == 0 || c.State != "OPEN" { // drafts, PRs, closed issues
				continue
			}
			task := attendance.Task{Repo: c.Repository.NameWithOwner, Number: c.Number, Title: c.Title, URL: c.URL, Updated: c.UpdatedAt}
			if n.Status != nil {
				task.Status = n.Status.Name
			}
			if task.Status == attendance.StatusDone {
				continue
			}
			if n.Priority != nil {
				task.Priority = n.Priority.Name
			}
			if n.Due != nil {
				task.Due = strings.TrimSpace(n.Due.Date)
			}
			for _, a := range c.Assignees.Nodes {
				task.Assignees = append(task.Assignees, a.Login)
			}
			out = append(out, task)
		}
		if !items.PageInfo.HasNextPage {
			return out, nil
		}
		vars["after"] = items.PageInfo.EndCursor
	}
	t.log.Warn("github: project items truncated", "team_id", teamID, "pages", maxProjectPages)
	return out, nil
}

// BranchActive implements attendance.TeamTaskSource: whether any branch of
// repo named `<number>-…` has a commit since since (any author). Only the
// first 100 branches are checked.
func (t *Tasks) BranchActive(ctx context.Context, teamID, repo string, number int, since time.Time) (bool, error) {
	tp, ok := t.teams[teamID]
	if !ok {
		return false, attendance.ErrGitHubNotSetUp
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || !strings.EqualFold(owner, tp.Org) {
		return false, fmt.Errorf("repo %q not in org", repo)
	}
	rp := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	r, err := t.app.rest(ctx, tp.Org, http.MethodGet, rp+"/branches?per_page=100", nil, true)
	if err != nil {
		return false, err
	}
	if r.status/100 != 2 {
		return false, apiErr(rp+"/branches", r)
	}
	var branches []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(r.body, &branches); err != nil {
		return false, fmt.Errorf("branches: %w", err)
	}
	prefix := strconv.Itoa(number) + "-"
	for _, b := range branches {
		if !strings.HasPrefix(b.Name, prefix) {
			continue
		}
		q := url.Values{"sha": {b.Name}, "since": {since.UTC().Format(time.RFC3339)}, "per_page": {"1"}}
		cr, err := t.app.rest(ctx, tp.Org, http.MethodGet, rp+"/commits?"+q.Encode(), nil, true)
		if err != nil {
			return false, err
		}
		if cr.status/100 != 2 {
			return false, apiErr(rp+"/commits", cr)
		}
		var commits []json.RawMessage
		if err := json.Unmarshal(cr.body, &commits); err != nil {
			return false, fmt.Errorf("commits: %w", err)
		}
		if len(commits) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// maxCountLogins bounds one ClosedCounts query (aliases per request).
const maxCountLogins = 50

// ClosedCounts implements attendance.TeamTaskSource: per login, the issues
// in teamID's org assigned to them and closed in [from, to].
func (t *Tasks) ClosedCounts(ctx context.Context, teamID string, logins []string, from, to time.Time) (map[string]int, error) {
	tp, ok := t.teams[teamID]
	if !ok {
		return nil, attendance.ErrGitHubNotSetUp
	}
	out := map[string]int{}
	rng := from.Format(time.RFC3339) + ".." + to.Format(time.RFC3339)
	for start := 0; start < len(logins); start += maxCountLogins {
		batch := logins[start:min(start+maxCountLogins, len(logins))]
		var decl, body []string
		vars := map[string]any{}
		for i, login := range batch {
			if !attendance.ValidGitHubLogin(login) {
				return nil, fmt.Errorf("invalid github login %q", login)
			}
			v := "q" + strconv.Itoa(i)
			decl = append(decl, "$"+v+": String!")
			body = append(body, fmt.Sprintf("  u%d: search(type: ISSUE, query: $%s, first: 0) { issueCount }", i, v))
			vars[v] = fmt.Sprintf("org:%s assignee:%s is:issue is:closed closed:%s", tp.Org, login, rng)
		}
		if len(batch) == 0 {
			break
		}
		q := "query(" + strings.Join(decl, ", ") + ") {\n" + strings.Join(body, "\n") + "\n}"
		var res map[string]struct {
			IssueCount int `json:"issueCount"`
		}
		if err := t.app.GraphQL(ctx, tp.Org, q, vars, &res, false); err != nil {
			return nil, err
		}
		for i, login := range batch {
			out[login] = res["u"+strconv.Itoa(i)].IssueCount
		}
	}
	return out, nil
}
