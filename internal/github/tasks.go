package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

// TeamProject is the GitHub org and Project a Mattermost team uses.
type TeamProject struct {
	Org    string
	Number int
}

// Tasks implements attendance.TaskSource on top of an App.
type Tasks struct {
	app   *App
	teams map[string]TeamProject // Mattermost team id → project
	log   *slog.Logger
	now   func() time.Time

	// SchemaTTL is how long a Project's field ids are cached; a failed
	// lookup is retried after SchemaRetry.
	SchemaTTL, SchemaRetry time.Duration

	mu      sync.Mutex
	schemas map[TeamProject]schemaEntry
}

type schemaEntry struct {
	schema  *Schema
	err     error
	fetched time.Time
}

var _ attendance.TaskSource = (*Tasks)(nil)

// NewTasks builds the task source. teams without an org are left out.
func NewTasks(app *App, teams map[string]TeamProject, log *slog.Logger) *Tasks {
	t := &Tasks{
		app: app, teams: map[string]TeamProject{}, log: log, now: time.Now,
		SchemaTTL: 6 * time.Hour, SchemaRetry: 5 * time.Minute,
		schemas: map[TeamProject]schemaEntry{},
	}
	for id, tp := range teams {
		if tp.Org != "" && tp.Number > 0 {
			t.teams[id] = tp
		}
	}
	return t
}

func (t *Tasks) Configured(teamID string) bool { _, ok := t.teams[teamID]; return ok }

func (t *Tasks) OrgName(teamID string) string { return t.teams[teamID].Org }

// Warm looks up every team's Project once and logs the result, so a
// misconfigured Project shows up in the log at startup.
func (t *Tasks) Warm(ctx context.Context) {
	for id, tp := range t.teams {
		s, err := t.Schema(ctx, id)
		if err != nil {
			t.log.Error("github project check failed: task features for this team won't work until it's fixed",
				"team_id", id, "org", tp.Org, "project", tp.Number, "err", err)
			continue
		}
		t.log.Info("github project ok", "team_id", id, "org", tp.Org, "project", tp.Number, "title", s.Title,
			"estimate_field", s.Estimate != nil)
	}
}

// Schema returns teamID's Project field ids, cached for SchemaTTL.
func (t *Tasks) Schema(ctx context.Context, teamID string) (*Schema, error) {
	tp, ok := t.teams[teamID]
	if !ok {
		return nil, attendance.ErrGitHubNotSetUp
	}
	t.mu.Lock()
	e, ok := t.schemas[tp]
	t.mu.Unlock()
	now := t.now()
	if ok {
		if e.err == nil && now.Sub(e.fetched) < t.SchemaTTL {
			return e.schema, nil
		}
		if e.err != nil && now.Sub(e.fetched) < t.SchemaRetry {
			return nil, e.err
		}
	}
	s, err := t.app.FetchSchema(ctx, tp.Org, tp.Number)
	if err != nil && !errors.Is(err, ErrSchema) {
		return nil, err // transient (network, rate limit): don't cache
	}
	if err != nil {
		err = fmt.Errorf("%w: %v", attendance.ErrGitHubNotSetUp, err)
	}
	t.mu.Lock()
	t.schemas[tp] = schemaEntry{schema: s, err: err, fetched: now}
	t.mu.Unlock()
	return s, err
}

// InvalidateSchema forgets teamID's cached field ids (e.g. after a write
// fails because a field or option id changed).
func (t *Tasks) InvalidateSchema(teamID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.schemas, t.teams[teamID])
}

// VerifyLogin checks the user exists and belongs to the team's org.
func (t *Tasks) VerifyLogin(ctx context.Context, teamID, login string) (string, error) {
	tp, ok := t.teams[teamID]
	if !ok {
		return "", attendance.ErrGitHubNotSetUp
	}
	path := "/users/" + url.PathEscape(login)
	r, err := t.app.rest(ctx, tp.Org, http.MethodGet, path, nil, true)
	if err != nil {
		return "", err
	}
	if r.status == http.StatusNotFound {
		return "", attendance.ErrGitHubUserNotFound
	}
	if r.status/100 != 2 {
		return "", apiErr(path, r)
	}
	var u struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	}
	if err := json.Unmarshal(r.body, &u); err != nil {
		return "", fmt.Errorf("github %s: %w", path, err)
	}
	if u.Type != "User" {
		return "", attendance.ErrGitHubUserNotFound // an org or bot account
	}

	path = "/orgs/" + url.PathEscape(tp.Org) + "/members/" + url.PathEscape(u.Login)
	r, err = t.app.rest(ctx, tp.Org, http.MethodGet, path, nil, true)
	if err != nil {
		return "", err
	}
	switch r.status {
	case http.StatusNoContent:
		return u.Login, nil
	case http.StatusNotFound:
		return "", attendance.ErrNotOrgMember
	case http.StatusFound:
		return "", fmt.Errorf("github %s: redirected: the App needs the Organization → Members (read) permission", path)
	}
	return "", apiErr(path, r)
}

const assignedQuery = `query($q: String!, $after: String) {
  search(type: ISSUE, query: $q, first: 50, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on Issue {
        number
        title
        url
        updatedAt
        repository { nameWithOwner }
        projectItems(first: 20) {
          nodes {
            project { id }
            status: fieldValueByName(name: "Status") { ... on ProjectV2ItemFieldSingleSelectValue { name } }
            priority: fieldValueByName(name: "Priority") { ... on ProjectV2ItemFieldSingleSelectValue { name } }
            due: fieldValueByName(name: "Due date") { ... on ProjectV2ItemFieldDateValue { date } }
          }
        }
      }
    }
  }
}`

// maxSearchPages caps how many issues AssignedTasks reads (50 per page).
const maxSearchPages = 4

type searchPage struct {
	Search struct {
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []issueNode `json:"nodes"`
	} `json:"search"`
}

type issueNode struct {
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	ProjectItems struct {
		Nodes []struct {
			Project struct {
				ID string `json:"id"`
			} `json:"project"`
			Status   *struct{ Name string } `json:"status"`
			Priority *struct{ Name string } `json:"priority"`
			Due      *struct{ Date string } `json:"due"`
		} `json:"nodes"`
	} `json:"projectItems"`
}

// AssignedTasks returns login's open issues in the team's org that are on
// the team's Project, excluding Status Done.
func (t *Tasks) AssignedTasks(ctx context.Context, teamID, login string) ([]attendance.Task, error) {
	tp, ok := t.teams[teamID]
	if !ok {
		return nil, attendance.ErrGitHubNotSetUp
	}
	if !attendance.ValidGitHubLogin(login) {
		return nil, fmt.Errorf("invalid github login %q", login)
	}
	schema, err := t.Schema(ctx, teamID)
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf("org:%s assignee:%s is:issue is:open archived:false", tp.Org, login)
	var out []attendance.Task
	vars := map[string]any{"q": q, "after": nil}
	for page := 0; page < maxSearchPages; page++ {
		var p searchPage
		if err := t.app.GraphQL(ctx, tp.Org, assignedQuery, vars, &p, false); err != nil {
			return nil, err
		}
		for _, n := range p.Search.Nodes {
			if task, ok := taskFromNode(n, schema.ProjectID); ok {
				out = append(out, task)
			}
		}
		if !p.Search.PageInfo.HasNextPage {
			return out, nil
		}
		vars["after"] = p.Search.PageInfo.EndCursor
	}
	t.log.Warn("github: assigned issues truncated", "team_id", teamID, "login", login, "pages", maxSearchPages)
	return out, nil
}

// taskFromNode keeps an issue only if it's on projectID and not Done.
func taskFromNode(n issueNode, projectID string) (attendance.Task, bool) {
	if n.Number == 0 { // a search hit that isn't an issue
		return attendance.Task{}, false
	}
	for _, it := range n.ProjectItems.Nodes {
		if it.Project.ID != projectID {
			continue
		}
		task := attendance.Task{
			Repo: n.Repository.NameWithOwner, Number: n.Number, Title: n.Title,
			URL: n.URL, Updated: n.UpdatedAt,
		}
		if it.Status != nil {
			task.Status = it.Status.Name
		}
		if it.Priority != nil {
			task.Priority = it.Priority.Name
		}
		if it.Due != nil {
			task.Due = strings.TrimSpace(it.Due.Date)
		}
		if task.Status == attendance.StatusDone {
			return attendance.Task{}, false
		}
		return task, true
	}
	return attendance.Task{}, false
}
