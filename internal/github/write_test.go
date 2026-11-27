package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

const issueURL = "https://github.com/inkyank-org/app/issues/42"

// issueResource is a recorded `resource(url:)` answer for #42.
func issueResource(assignee, state string, labels []string, onProject bool) string {
	return issueResourceStatus(assignee, state, labels, onProject, "")
}

// issueResourceStatus is issueResource with a Status on the team's Project.
func issueResourceStatus(assignee, state string, labels []string, onProject bool, status string) string {
	var ls []string
	for _, l := range labels {
		ls = append(ls, fmt.Sprintf(`{"name":%q}`, l))
	}
	items := `[{"id":"PVTI_other","project":{"id":"PVT_other"}}]`
	if onProject {
		st := "null"
		if status != "" {
			st = fmt.Sprintf(`{"name":%q}`, status)
		}
		items = `[{"id":"PVTI_42","project":{"id":"PVT_kwDOinkyank"},"status":` + st + `}]`
	}
	return fmt.Sprintf(`{"data":{"resource":{"id":"I_42","number":42,"title":"Session timeout","state":%q,
 "repository":{"name":"app","owner":{"login":"inkyank-org"}},
 "assignees":{"nodes":[{"login":%q}]},"labels":{"nodes":[%s]},
 "projectItems":{"nodes":%s}}}}`, state, assignee, strings.Join(ls, ","), items)
}

// writeFake scripts the endpoints ApplyEODStatus uses and records them.
type writeFake struct {
	resource  string
	mutations []string          // mutation names, with key vars
	rest      []string          // "METHOD path"
	bodies    map[string]string // path → body
}

func (wf *writeFake) handler(t *testing.T) func(http.ResponseWriter, *http.Request, []byte) bool {
	return func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if r.URL.Path == "/graphql" {
			var q struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			_ = json.Unmarshal(body, &q)
			switch {
			case strings.Contains(q.Query, "resource(url"):
				_, _ = io.WriteString(w, wf.resource)
			case strings.Contains(q.Query, "updateProjectV2ItemFieldValue"):
				wf.mutations = append(wf.mutations, fmt.Sprintf("status item=%v field=%v option=%v", q.Variables["i"], q.Variables["f"], q.Variables["o"]))
				_, _ = io.WriteString(w, `{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"x"}}}}`)
			case strings.Contains(q.Query, "addProjectV2ItemById"):
				wf.mutations = append(wf.mutations, "add-to-project")
				_, _ = io.WriteString(w, `{"data":{"addProjectV2ItemById":{"item":{"id":"PVTI_new"}}}}`)
			case strings.Contains(q.Query, "closeIssue"):
				wf.mutations = append(wf.mutations, "close")
				_, _ = io.WriteString(w, `{"data":{"closeIssue":{"issue":{"state":"CLOSED"}}}}`)
			default:
				return false // the fields query: default fixture
			}
			return true
		}
		wf.rest = append(wf.rest, r.Method+" "+r.URL.Path)
		if wf.bodies == nil {
			wf.bodies = map[string]string{}
		}
		wf.bodies[r.URL.Path] = string(body)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/inkyank-org/app/labels":
			w.WriteHeader(http.StatusUnprocessableEntity) // already exists
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = io.WriteString(w, `{}`)
		return true
	}
}

func TestApplyEODStatusRefusesOthersIssues(t *testing.T) {
	f, srv := newFake(t)
	wf := &writeFake{resource: issueResource("someone-else", "OPEN", nil, true)}
	f.extra = wf.handler(t)
	_, err := newTestTasks(t, srv).ApplyEODStatus(context.Background(), "A", "Jana-Dev", issueURL, "Done", "c")
	if !errors.Is(err, attendance.ErrNotAssigned) {
		t.Fatalf("want ErrNotAssigned, got %v", err)
	}
	if len(wf.mutations) != 0 || len(wf.rest) != 0 {
		t.Errorf("nothing may be written: %v %v", wf.mutations, wf.rest)
	}
}

func TestApplyEODStatusBlockedThenDone(t *testing.T) {
	f, srv := newFake(t)
	wf := &writeFake{resource: issueResource("jana-dev", "OPEN", nil, true)} // login matched case-insensitively
	f.extra = wf.handler(t)
	tasks := newTestTasks(t, srv)

	if closed, err := tasks.ApplyEODStatus(context.Background(), "A", "Jana-Dev", issueURL, "Blocked", "📝 EOD"); err != nil || closed {
		t.Fatal(closed, err)
	}
	if strings.Join(wf.mutations, ";") != "status item=PVTI_42 field=PVTSSF_status option=b1c2d3e4" {
		t.Errorf("mutations: %v", wf.mutations)
	}
	want := "POST /repos/inkyank-org/app/labels;POST /repos/inkyank-org/app/issues/42/labels;POST /repos/inkyank-org/app/issues/42/comments"
	if strings.Join(wf.rest, ";") != want {
		t.Errorf("rest:\n%v\nwant\n%v", wf.rest, want)
	}
	if !strings.Contains(wf.bodies["/repos/inkyank-org/app/issues/42/labels"], `"blocked"`) ||
		!strings.Contains(wf.bodies["/repos/inkyank-org/app/issues/42/comments"], "📝 EOD") {
		t.Errorf("bodies: %v", wf.bodies)
	}

	// Done on a blocked issue: status, label removed, issue closed, comment.
	wf.mutations, wf.rest = nil, nil
	wf.resource = issueResource("Jana-Dev", "OPEN", []string{"Blocked", "bug"}, true)
	if closed, err := tasks.ApplyEODStatus(context.Background(), "A", "Jana-Dev", issueURL, "Done", "done"); err != nil || !closed {
		t.Fatal(closed, err)
	}
	if strings.Join(wf.mutations, ";") != "status item=PVTI_42 field=PVTSSF_status option=98236657;close" {
		t.Errorf("mutations: %v", wf.mutations)
	}
	if strings.Join(wf.rest, ";") != "DELETE /repos/inkyank-org/app/issues/42/labels/blocked;POST /repos/inkyank-org/app/issues/42/comments" {
		t.Errorf("rest: %v", wf.rest)
	}

	// Not on the team's Project yet: it's added first. Closed issue: not closed again.
	wf.mutations, wf.rest = nil, nil
	wf.resource = issueResource("Jana-Dev", "CLOSED", nil, false)
	if closed, err := tasks.ApplyEODStatus(context.Background(), "A", "Jana-Dev", issueURL, "Done", "done"); err != nil || closed {
		t.Fatal(closed, err)
	}
	if strings.Join(wf.mutations, ";") != "add-to-project;status item=PVTI_new field=PVTSSF_status option=98236657" {
		t.Errorf("mutations: %v", wf.mutations)
	}
	if _, err := tasks.ApplyEODStatus(context.Background(), "A", "Jana-Dev", issueURL, "Shipped", "x"); err == nil {
		t.Error("unknown status accepted")
	}
}

func TestActivity(t *testing.T) {
	f, srv := newFake(t)
	ist := time.FixedZone("IST", 5*3600+1800)
	day := time.Date(2026, 11, 26, 0, 0, 0, 0, ist)
	var commitQuery, countVars string
	f.extra = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		switch {
		case r.URL.Path == "/graphql" && strings.Contains(string(body), "reviewed: search"):
			countVars = string(body)
			_, _ = io.WriteString(w, `{"data":{"closed":{"issueCount":1,"nodes":[{"url":"https://github.com/inkyank-org/app/issues/40"}]},"opened":{"issueCount":0},"merged":{"issueCount":0},"reviewed":{"issueCount":0}}}`)
		case r.URL.Path == "/search/commits":
			commitQuery = r.URL.Query().Get("q")
			_, _ = io.WriteString(w, `{"total_count":6,"items":[{"repository":{"full_name":"inkyank-org/app"}},{"repository":{"full_name":"inkyank-org/app"}},{"repository":{"full_name":"inkyank-org/web"}}]}`)
		case r.URL.Path == "/orgs/inkyank-org/repos":
			_, _ = io.WriteString(w, `[
 {"name":"app","full_name":"inkyank-org/app","pushed_at":"2026-11-26T09:00:00Z"},
 {"name":"old","full_name":"inkyank-org/old","pushed_at":"2026-11-20T09:00:00Z"}]`)
		case r.URL.Path == "/repos/inkyank-org/app/branches":
			_, _ = io.WriteString(w, `[{"name":"main"},{"name":"42-session-timeout"},{"name":"45-wfh"},{"name":"feature-x"}]`)
		case r.URL.Path == "/repos/inkyank-org/app/commits":
			q := r.URL.Query()
			if q.Get("author") != "Jana-Dev" || q.Get("since") != "2026-11-25T18:30:00Z" || q.Get("until") != "2026-11-26T18:29:59Z" {
				t.Errorf("commit filter: %v", q)
			}
			if q.Get("sha") == "42-session-timeout" {
				_, _ = io.WriteString(w, `[{"sha":"abc"}]`)
			} else {
				_, _ = io.WriteString(w, `[]`)
			}
		case r.URL.Path == "/repos/inkyank-org/old/branches":
			t.Error("repo not pushed that day was checked")
			return false
		default:
			return false
		}
		return true
	}
	a, err := newTestTasks(t, srv).Activity(context.Background(), "A", "Jana-Dev", day)
	if err != nil {
		t.Fatal(err)
	}
	if a.Commits != 6 || a.CommitRepos["inkyank-org/app"] != 2 || a.IssuesClosed != 1 || a.PRsOpened != 0 ||
		len(a.ClosedIssues) != 1 || a.ClosedIssues[0] != "https://github.com/inkyank-org/app/issues/40" {
		t.Errorf("activity: %+v", a)
	}
	// Only branches named <number>-… with the member's commits that day.
	if len(a.Branches) != 1 || a.Branches[0].Issue != 42 || a.Branches[0].Branch != "42-session-timeout" {
		t.Errorf("branches: %+v", a.Branches)
	}
	if commitQuery != "author:Jana-Dev org:inkyank-org committer-date:2026-11-26T00:00:00+05:30..2026-11-26T23:59:59+05:30" {
		t.Errorf("commit search uses the IST day: %q", commitQuery)
	}
	if !strings.Contains(countVars, "is:pr is:merged merged:2026-11-26T00:00:00+05:30..2026-11-26T23:59:59+05:30") {
		t.Errorf("count vars: %s", countVars)
	}
	if got := attendance.RenderActivity(a); got != "GitHub: 6 commits to default branches · 1 issue closed · branch: 42" {
		t.Errorf("rendered (PR counts omitted when zero): %q", got)
	}
}

func TestRateLimitLow(t *testing.T) {
	for _, c := range []struct {
		remaining int
		limit     string
		want      bool
	}{
		{25, "30", false}, // search: plenty left, no warning
		{2, "30", true},
		{600, "5000", false},
		{499, "5000", true},
		{50, "", true}, // no limit header: fixed threshold
		{150, "", false},
	} {
		if got := rateLimitLow(c.remaining, c.limit); got != c.want {
			t.Errorf("rateLimitLow(%d, %q) = %v", c.remaining, c.limit, got)
		}
	}
}

func TestStartTask(t *testing.T) {
	f, srv := newFake(t)
	wf := &writeFake{resource: issueResourceStatus("Jana-Dev", "OPEN", nil, true, "Todo")}
	f.extra = wf.handler(t)
	tasks := newTestTasks(t, srv)
	ctx := context.Background()

	// Todo → In progress; no comment, no labels.
	p, err := tasks.StartTask(ctx, "A", "jana-dev", issueURL)
	if err != nil || !p.Started || p.Status != "In progress" || p.Number != 42 || p.Title != "Session timeout" {
		t.Fatalf("pick: %+v %v", p, err)
	}
	if strings.Join(wf.mutations, ";") != "status item=PVTI_42 field=PVTSSF_status option=47fc9ee4" || len(wf.rest) != 0 {
		t.Errorf("writes: %v %v", wf.mutations, wf.rest)
	}

	// Already In review or Blocked: left as is.
	for _, st := range []string{"In review", "Blocked", "In progress"} {
		wf.mutations = nil
		wf.resource = issueResourceStatus("Jana-Dev", "OPEN", nil, true, st)
		p, err := tasks.StartTask(ctx, "A", "Jana-Dev", issueURL)
		if err != nil || p.Started || p.Status != st || len(wf.mutations) != 0 {
			t.Errorf("%s: %+v %v %v", st, p, err, wf.mutations)
		}
	}

	// Not on the team's Project: refused, not added (the menu never offers it).
	wf.mutations = nil
	wf.resource = issueResource("Jana-Dev", "OPEN", nil, false)
	if _, err := tasks.StartTask(ctx, "A", "Jana-Dev", issueURL); !errors.Is(err, attendance.ErrNotOnProject) || len(wf.mutations) != 0 {
		t.Errorf("not on project: %v %v", err, wf.mutations)
	}

	// Someone else's issue, or a closed one: nothing written.
	wf.mutations = nil
	wf.resource = issueResourceStatus("someone-else", "OPEN", nil, true, "Todo")
	if _, err := tasks.StartTask(ctx, "A", "Jana-Dev", issueURL); !errors.Is(err, attendance.ErrNotAssigned) {
		t.Errorf("want ErrNotAssigned, got %v", err)
	}
	wf.resource = issueResourceStatus("Jana-Dev", "CLOSED", nil, true, "Done")
	if _, err := tasks.StartTask(ctx, "A", "Jana-Dev", issueURL); !errors.Is(err, attendance.ErrIssueClosed) {
		t.Errorf("want ErrIssueClosed, got %v", err)
	}
	if len(wf.mutations) != 0 {
		t.Errorf("refused picks wrote: %v", wf.mutations)
	}
}
