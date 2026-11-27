package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

var _ attendance.TaskWriter = (*Tasks)(nil)

const blockedLabel = "blocked"

const issueQuery = `query($url: URI!) {
  resource(url: $url) {
    ... on Issue {
      id
      number
      state
      repository { name owner { login } }
      assignees(first: 20) { nodes { login } }
      labels(first: 50) { nodes { name } }
      title
      projectItems(first: 20) {
        nodes {
          id
          project { id }
          status: fieldValueByName(name: "Status") { ... on ProjectV2ItemFieldSingleSelectValue { name } }
        }
      }
    }
  }
}`

type issueInfo struct {
	ID         string `json:"id"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	ProjectItems struct {
		Nodes []struct {
			ID      string `json:"id"`
			Project struct {
				ID string `json:"id"`
			} `json:"project"`
			Status *struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"nodes"`
	} `json:"projectItems"`
}

func (i *issueInfo) repoPath() string {
	return "/repos/" + url.PathEscape(i.Repository.Owner.Login) + "/" + url.PathEscape(i.Repository.Name)
}

// ApplyEODStatus implements attendance.TaskWriter. Every step is safe to
// repeat, and the comment comes last, so a retry after a partial failure
// doesn't post it twice. closed reports that this call closed the issue.
func (t *Tasks) ApplyEODStatus(ctx context.Context, teamID, login, issueURL, status, comment string) (closed bool, err error) {
	tp, ok := t.teams[teamID]
	if !ok {
		return false, attendance.ErrGitHubNotSetUp
	}
	schema, err := t.Schema(ctx, teamID)
	if err != nil {
		return false, err
	}
	optionID, ok := schema.Status.Options[status]
	if !ok {
		return false, fmt.Errorf("unknown status %q", status)
	}

	is, err := t.assignedIssue(ctx, tp, login, issueURL)
	if err != nil {
		return false, err
	}
	itemID, err := t.projectItem(ctx, tp, schema, is)
	if err != nil {
		return false, err
	}
	if err := t.setStatus(ctx, teamID, schema, itemID, optionID); err != nil {
		return false, err
	}

	hasBlocked := false
	for _, l := range is.Labels.Nodes {
		if strings.EqualFold(l.Name, blockedLabel) {
			hasBlocked = true
		}
	}
	switch {
	case status == attendance.StatusBlocked && !hasBlocked:
		if err := t.addBlockedLabel(ctx, tp.Org, is); err != nil {
			return false, err
		}
	case status != attendance.StatusBlocked && hasBlocked:
		path := fmt.Sprintf("%s/issues/%d/labels/%s", is.repoPath(), is.Number, blockedLabel)
		r, err := t.app.rest(ctx, tp.Org, http.MethodDelete, path, nil, true)
		if err != nil {
			return false, err
		}
		if r.status/100 != 2 && r.status != http.StatusNotFound {
			return false, apiErr(path, r)
		}
	}

	if status == attendance.StatusDone && is.State == "OPEN" {
		if err := t.app.GraphQL(ctx, tp.Org, `mutation($id: ID!) {
  closeIssue(input: {issueId: $id, stateReason: COMPLETED}) { issue { state } }
}`, map[string]any{"id": is.ID}, nil, true); err != nil {
			return false, fmt.Errorf("close issue: %w", err)
		}
		closed = true
	}

	path := fmt.Sprintf("%s/issues/%d/comments", is.repoPath(), is.Number)
	r, err := t.app.rest(ctx, tp.Org, http.MethodPost, path, map[string]string{"body": comment}, false)
	if err != nil {
		return closed, err
	}
	if r.status/100 != 2 {
		return closed, apiErr(path, r)
	}
	return closed, nil
}

// assignedIssue loads the issue and refuses it (ErrNotAssigned) unless it's
// assigned to login and in the team's org (spec §11).
func (t *Tasks) assignedIssue(ctx context.Context, tp TeamProject, login, issueURL string) (*issueInfo, error) {
	var out struct {
		Resource *issueInfo `json:"resource"`
	}
	if err := t.app.GraphQL(ctx, tp.Org, issueQuery, map[string]any{"url": issueURL}, &out, false); err != nil {
		return nil, err
	}
	is := out.Resource
	if is == nil || is.ID == "" {
		return nil, fmt.Errorf("issue %s not found", issueURL)
	}
	assigned := false
	for _, a := range is.Assignees.Nodes {
		if strings.EqualFold(a.Login, login) {
			assigned = true
		}
	}
	if !assigned || !strings.EqualFold(is.Repository.Owner.Login, tp.Org) {
		return nil, attendance.ErrNotAssigned
	}
	return is, nil
}

// projectStatus is the issue's item and Status on the team's Project, both
// empty when the issue isn't on it.
func projectStatus(is *issueInfo, projectID string) (itemID, status string) {
	for _, it := range is.ProjectItems.Nodes {
		if it.Project.ID == projectID {
			if it.Status != nil {
				status = it.Status.Name
			}
			return it.ID, status
		}
	}
	return "", ""
}

// projectItem returns the issue's item on the team's Project, adding the
// issue to the Project first if needed.
func (t *Tasks) projectItem(ctx context.Context, tp TeamProject, schema *Schema, is *issueInfo) (string, error) {
	if itemID, _ := projectStatus(is, schema.ProjectID); itemID != "" {
		return itemID, nil
	}
	var add struct {
		AddProjectV2ItemByID struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"addProjectV2ItemById"`
	}
	if err := t.app.GraphQL(ctx, tp.Org, `mutation($p: ID!, $c: ID!) {
  addProjectV2ItemById(input: {projectId: $p, contentId: $c}) { item { id } }
}`, map[string]any{"p": schema.ProjectID, "c": is.ID}, &add, true); err != nil {
		return "", fmt.Errorf("add to project: %w", err)
	}
	return add.AddProjectV2ItemByID.Item.ID, nil
}

// setStatus sets the item's Status option.
func (t *Tasks) setStatus(ctx context.Context, teamID string, schema *Schema, itemID, optionID string) error {
	if err := t.app.GraphQL(ctx, t.teams[teamID].Org, `mutation($p: ID!, $i: ID!, $f: ID!, $o: String!) {
  updateProjectV2ItemFieldValue(input: {projectId: $p, itemId: $i, fieldId: $f, value: {singleSelectOptionId: $o}}) { projectV2Item { id } }
}`, map[string]any{"p": schema.ProjectID, "i": itemID, "f": schema.Status.ID, "o": optionID}, nil, true); err != nil {
		var ge *GraphQLError
		if errors.As(err, &ge) { // stale field/option ids: look them up again next time
			t.InvalidateSchema(teamID)
		}
		return fmt.Errorf("set status: %w", err)
	}
	return nil
}

// StartTask implements attendance.TaskWriter: a member picked the issue as
// today's work. It moves it to In progress unless it's already In progress,
// In review or Blocked (a pick never moves work backwards). Issues not on
// the team's Project are refused (ErrNotOnProject).
func (t *Tasks) StartTask(ctx context.Context, teamID, login, issueURL string) (attendance.Pick, error) {
	tp, ok := t.teams[teamID]
	if !ok {
		return attendance.Pick{}, attendance.ErrGitHubNotSetUp
	}
	schema, err := t.Schema(ctx, teamID)
	if err != nil {
		return attendance.Pick{}, err
	}
	optionID, ok := schema.Status.Options[attendance.StatusInProgress]
	if !ok {
		return attendance.Pick{}, attendance.ErrGitHubNotSetUp
	}
	is, err := t.assignedIssue(ctx, tp, login, issueURL)
	if err != nil {
		return attendance.Pick{}, err
	}
	itemID, current := projectStatus(is, schema.ProjectID)
	p := attendance.Pick{Number: is.Number, Title: is.Title, Status: current}
	// The menu only offers issues on the team's Project: anything else is a
	// tampered value (e.g. another team's issue in a shared org).
	if itemID == "" {
		return p, attendance.ErrNotOnProject
	}
	if is.State != "OPEN" {
		return p, attendance.ErrIssueClosed
	}
	switch current {
	case attendance.StatusInProgress, attendance.StatusInReview, attendance.StatusBlocked:
		return p, nil
	}
	if err := t.setStatus(ctx, teamID, schema, itemID, optionID); err != nil {
		return p, err
	}
	p.Status, p.Started = attendance.StatusInProgress, true
	return p, nil
}

// addBlockedLabel creates the repo's "blocked" label if needed, then adds it.
func (t *Tasks) addBlockedLabel(ctx context.Context, org string, is *issueInfo) error {
	path := is.repoPath() + "/labels"
	r, err := t.app.rest(ctx, org, http.MethodPost, path, map[string]string{
		"name": blockedLabel, "color": "B60205", "description": "Work can't continue (set by the attendance bot with Status: Blocked)",
	}, false)
	if err != nil {
		return err
	}
	if r.status/100 != 2 && r.status != http.StatusUnprocessableEntity { // 422: already exists
		return apiErr(path, r)
	}
	path = fmt.Sprintf("%s/issues/%d/labels", is.repoPath(), is.Number)
	r, err = t.app.rest(ctx, org, http.MethodPost, path, map[string][]string{"labels": {blockedLabel}}, true)
	if err != nil {
		return err
	}
	if r.status/100 != 2 {
		return apiErr(path, r)
	}
	return nil
}

// --- activity ---

// Activity limits: how many recently pushed repos and numbered branches
// per repo are checked for the member's commits.
const (
	maxActivityRepos    = 10
	maxActivityBranches = 30
)

const activityCountsQuery = `query($closed: String!, $opened: String!, $merged: String!, $reviewed: String!) {
  closed: search(type: ISSUE, query: $closed, first: 100) { issueCount nodes { ... on Issue { url } } }
  opened: search(type: ISSUE, query: $opened, first: 0) { issueCount }
  merged: search(type: ISSUE, query: $merged, first: 0) { issueCount }
  reviewed: search(type: ISSUE, query: $reviewed, first: 0) { issueCount }
}`

// Activity implements attendance.TaskWriter. Commits are matched by the
// member's GitHub login: commits made with a git email that isn't linked
// to their GitHub account aren't counted.
//
//   - commits: GitHub commit search (default branches only) in the org,
//     by author, committed during the local day;
//   - branches: in up to 10 repos pushed that day, branches named
//     `<number>-…` with commits by the member that day;
//   - issues closed: assigned to the member, closed that day;
//   - PRs opened / merged by the member that day, and PRs they reviewed
//     that were updated that day (an approximation of reviews submitted).
func (t *Tasks) Activity(ctx context.Context, teamID, login string, dayStart time.Time) (*attendance.Activity, error) {
	tp, ok := t.teams[teamID]
	if !ok {
		return nil, attendance.ErrGitHubNotSetUp
	}
	if !attendance.ValidGitHubLogin(login) {
		return nil, fmt.Errorf("invalid github login %q", login)
	}
	from := dayStart
	to := dayStart.AddDate(0, 0, 1).Add(-time.Second)
	rng := from.Format(time.RFC3339) + ".." + to.Format(time.RFC3339)
	act := &attendance.Activity{CommitRepos: map[string]int{}}

	// Commits on default branches.
	q := url.Values{"q": {fmt.Sprintf("author:%s org:%s committer-date:%s", login, tp.Org, rng)}, "per_page": {"100"}}
	path := "/search/commits?" + q.Encode()
	r, err := t.app.rest(ctx, tp.Org, http.MethodGet, path, nil, true)
	if err != nil {
		return nil, err
	}
	if r.status/100 != 2 {
		return nil, apiErr("/search/commits", r)
	}
	var cs struct {
		TotalCount int `json:"total_count"`
		Items      []struct {
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"items"`
	}
	if err := json.Unmarshal(r.body, &cs); err != nil {
		return nil, fmt.Errorf("commit search: %w", err)
	}
	act.Commits = cs.TotalCount
	for _, it := range cs.Items {
		act.CommitRepos[it.Repository.FullName]++
	}

	// Issue and PR counts in one query.
	var counts struct {
		Closed struct {
			IssueCount int `json:"issueCount"`
			Nodes      []struct {
				URL string `json:"url"`
			} `json:"nodes"`
		}
		Opened, Merged, Reviewed struct {
			IssueCount int `json:"issueCount"`
		}
	}
	vars := map[string]any{
		"closed":   fmt.Sprintf("org:%s assignee:%s is:issue is:closed closed:%s", tp.Org, login, rng),
		"opened":   fmt.Sprintf("org:%s author:%s is:pr created:%s", tp.Org, login, rng),
		"merged":   fmt.Sprintf("org:%s author:%s is:pr is:merged merged:%s", tp.Org, login, rng),
		"reviewed": fmt.Sprintf("org:%s reviewed-by:%s -author:%s is:pr updated:%s", tp.Org, login, login, rng),
	}
	if err := t.app.GraphQL(ctx, tp.Org, activityCountsQuery, vars, &counts, false); err != nil {
		return nil, err
	}
	act.IssuesClosed, act.PRsOpened, act.PRsMerged, act.Reviews =
		counts.Closed.IssueCount, counts.Opened.IssueCount, counts.Merged.IssueCount, counts.Reviewed.IssueCount
	for _, n := range counts.Closed.Nodes {
		act.ClosedIssues = append(act.ClosedIssues, n.URL)
	}

	branches, err := t.branchWork(ctx, tp.Org, login, from, to)
	if err != nil {
		return nil, err
	}
	act.Branches = branches
	return act, nil
}

// branchWork finds `<number>-…` branches the member committed to that day.
func (t *Tasks) branchWork(ctx context.Context, org, login string, from, to time.Time) ([]attendance.BranchWork, error) {
	path := "/orgs/" + url.PathEscape(org) + "/repos?sort=pushed&direction=desc&per_page=30"
	r, err := t.app.rest(ctx, org, http.MethodGet, path, nil, true)
	if err != nil {
		return nil, err
	}
	if r.status/100 != 2 {
		return nil, apiErr(path, r)
	}
	var repos []struct {
		Name     string    `json:"name"`
		FullName string    `json:"full_name"`
		PushedAt time.Time `json:"pushed_at"`
		Archived bool      `json:"archived"`
	}
	if err := json.Unmarshal(r.body, &repos); err != nil {
		return nil, fmt.Errorf("repos: %w", err)
	}
	var out []attendance.BranchWork
	checked := 0
	for _, repo := range repos {
		if repo.PushedAt.Before(from) {
			break // sorted by push time
		}
		if repo.Archived || checked >= maxActivityRepos {
			continue
		}
		checked++
		rp := "/repos/" + url.PathEscape(org) + "/" + url.PathEscape(repo.Name)
		br, err := t.app.rest(ctx, org, http.MethodGet, rp+"/branches?per_page=100", nil, true)
		if err != nil {
			return nil, err
		}
		if br.status/100 != 2 {
			return nil, apiErr(rp+"/branches", br)
		}
		var names []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(br.body, &names); err != nil {
			return nil, fmt.Errorf("branches: %w", err)
		}
		n := 0
		for _, b := range names {
			issue := attendance.BranchIssue(b.Name)
			if issue == 0 {
				continue // not linked to an issue
			}
			if n++; n > maxActivityBranches {
				t.log.Warn("github activity: branch limit reached", "repo", repo.FullName)
				break
			}
			q := url.Values{"sha": {b.Name}, "author": {login}, "since": {from.UTC().Format(time.RFC3339)},
				"until": {to.UTC().Format(time.RFC3339)}, "per_page": {"1"}}
			cr, err := t.app.rest(ctx, org, http.MethodGet, rp+"/commits?"+q.Encode(), nil, true)
			if err != nil {
				return nil, err
			}
			if cr.status/100 != 2 {
				return nil, apiErr(rp+"/commits", cr)
			}
			var commits []json.RawMessage
			if json.Unmarshal(cr.body, &commits) == nil && len(commits) > 0 {
				out = append(out, attendance.BranchWork{Repo: repo.FullName, Branch: b.Name, Issue: issue})
			}
		}
	}
	return out, nil
}
