package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProjectTasks(t *testing.T) {
	f, srv := newFake(t)
	pages := 0
	f.extra = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if r.URL.Path != "/graphql" || !strings.Contains(string(body), "items(first: 100") {
			return false
		}
		pages++
		if pages == 1 {
			_, _ = io.WriteString(w, `{"data":{"organization":{"projectV2":{"items":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[
 {"status":{"name":"In progress"},"priority":{"name":"High"},"due":{"date":"2026-11-20"},
  "content":{"number":12,"title":"Pricing","url":"https://github.com/inkyank-org/app/issues/12","state":"OPEN","updatedAt":"2026-11-18T10:00:00Z",
   "repository":{"nameWithOwner":"inkyank-org/app"},"assignees":{"nodes":[{"login":"Jana-Dev"},{"login":"vairabarath"}]}}},
 {"status":{"name":"Done"},"content":{"number":13,"title":"Done one","url":"u13","state":"OPEN","repository":{"nameWithOwner":"inkyank-org/app"},"assignees":{"nodes":[]}}},
 {"status":null,"content":{"number":14,"title":"Closed","url":"u14","state":"CLOSED","repository":{"nameWithOwner":"inkyank-org/app"},"assignees":{"nodes":[]}}},
 {"status":{"name":"Todo"},"content":{}}
]}}}}}`)
			return true
		}
		if !strings.Contains(string(body), `"after":"c1"`) {
			t.Errorf("second page without cursor: %s", body)
		}
		_, _ = io.WriteString(w, `{"data":{"organization":{"projectV2":{"items":{"pageInfo":{"hasNextPage":false},"nodes":[
 {"status":{"name":"Todo"},"content":{"number":20,"title":"Unassigned","url":"u20","state":"OPEN","repository":{"nameWithOwner":"inkyank-org/web"},"assignees":{"nodes":[]}}}
]}}}}}`)
		return true
	}
	tasks, err := newTestTasks(t, srv).ProjectTasks(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	// Done, closed issues and drafts/PRs (no number) are left out.
	if len(tasks) != 2 || tasks[0].Number != 12 || tasks[1].Number != 20 {
		t.Fatalf("tasks: %+v", tasks)
	}
	t12 := tasks[0]
	if t12.Status != "In progress" || t12.Priority != "High" || t12.Due != "2026-11-20" || t12.Repo != "inkyank-org/app" ||
		strings.Join(t12.Assignees, ",") != "Jana-Dev,vairabarath" || !t12.Updated.Equal(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("task 12: %+v", t12)
	}
}

func TestBranchActive(t *testing.T) {
	f, srv := newFake(t)
	var commitQueries []string
	f.extra = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		switch r.URL.Path {
		case "/repos/inkyank-org/app/branches":
			_, _ = io.WriteString(w, `[{"name":"main"},{"name":"12-pricing"},{"name":"120-other"},{"name":"12-pricing-v2"}]`)
		case "/repos/inkyank-org/app/commits":
			q := r.URL.Query()
			commitQueries = append(commitQueries, q.Get("sha")+"@"+q.Get("since"))
			if q.Get("sha") == "12-pricing-v2" {
				_, _ = io.WriteString(w, `[{"sha":"abc"}]`)
			} else {
				_, _ = io.WriteString(w, `[]`)
			}
		default:
			return false
		}
		return true
	}
	tasks := newTestTasks(t, srv)
	since := time.Date(2026, 11, 20, 0, 0, 0, 0, time.FixedZone("IST", 19800))
	active, err := tasks.BranchActive(context.Background(), "A", "inkyank-org/app", 12, since)
	if err != nil || !active {
		t.Fatalf("active=%v err=%v", active, err)
	}
	// Only `12-…` branches (not 120-…), since in UTC.
	if strings.Join(commitQueries, ",") != "12-pricing@2026-11-19T18:30:00Z,12-pricing-v2@2026-11-19T18:30:00Z" {
		t.Errorf("commit queries: %v", commitQueries)
	}
	if _, err := tasks.BranchActive(context.Background(), "A", "evil-org/app", 12, since); err == nil {
		t.Error("repo outside the org accepted")
	}
}

func TestClosedCounts(t *testing.T) {
	f, srv := newFake(t)
	var vars map[string]any
	f.extra = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if r.URL.Path != "/graphql" || !strings.Contains(string(body), "u1: search") {
			return false
		}
		var q struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &q)
		vars = q.Variables
		_, _ = io.WriteString(w, `{"data":{"u0":{"issueCount":4},"u1":{"issueCount":0}}}`)
		return true
	}
	ist := time.FixedZone("IST", 19800)
	from := time.Date(2026, 11, 23, 0, 0, 0, 0, ist)
	got, err := newTestTasks(t, srv).ClosedCounts(context.Background(), "A", []string{"Jana-Dev", "vairabarath"}, from, from.AddDate(0, 0, 7).Add(-time.Second))
	if err != nil || got["Jana-Dev"] != 4 || got["vairabarath"] != 0 {
		t.Fatalf("got %v %v", got, err)
	}
	if vars["q0"] != "org:inkyank-org assignee:Jana-Dev is:issue is:closed closed:2026-11-23T00:00:00+05:30..2026-11-29T23:59:59+05:30" {
		t.Errorf("query: %v", vars)
	}
	if _, err := newTestTasks(t, srv).ClosedCounts(context.Background(), "A", []string{"bad login!"}, from, from); err == nil {
		t.Error("invalid login accepted")
	}
}
