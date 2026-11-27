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

// An issue assigned to the member but not on the Project is listed as an
// OffBoard Todo task when issues may be added to the board — and dropped
// when they may not.
func TestAssignedTasksOffBoard(t *testing.T) {
	_, srv := newFake(t) // search fixtures: #42 on another project
	plain, err := newTestTasks(t, srv).AssignedTasks(context.Background(), "A", "Jana-Dev")
	if err != nil {
		t.Fatal(err)
	}
	tasks := newTestTasks(t, srv)
	tasks.AddAssigned = true
	all, err := tasks.AssignedTasks(context.Background(), "A", "Jana-Dev")
	if err != nil {
		t.Fatal(err)
	}
	var off []attendance.Task
	for _, tk := range all {
		if tk.OffBoard {
			off = append(off, tk)
			if tk.Status != attendance.StatusTodo {
				t.Errorf("off-board task status %q", tk.Status)
			}
		}
	}
	if len(off) == 0 || len(all) != len(plain)+len(off) {
		t.Fatalf("plain %d, with off-board %d (%d off)", len(plain), len(all), len(off))
	}
	for _, tk := range plain {
		if tk.OffBoard {
			t.Error("off-board task listed with adding off")
		}
	}
}

func TestAddToBoard(t *testing.T) {
	f, srv := newFake(t)
	wf := &writeFake{}
	f.extra = wf.handler(t)
	tasks := newTestTasks(t, srv)
	off := attendance.Task{URL: "u1", NodeID: "I_1", OffBoard: true}
	onBoard := attendance.Task{URL: "u2", NodeID: "I_2"}

	// Off: nothing happens.
	if n, err := tasks.AddToBoard(context.Background(), "A", []attendance.Task{off}); n != 0 || err != nil || len(wf.mutations) != 0 {
		t.Fatalf("adding off: %d %v %v", n, err, wf.mutations)
	}
	tasks.AddAssigned = true
	n, err := tasks.AddToBoard(context.Background(), "A", []attendance.Task{off, onBoard})
	// Added with no Status → set to Todo; tasks already on the board are skipped.
	if n != 1 || err != nil || strings.Join(wf.mutations, ";") != "add-to-project;status item=PVTI_new field=PVTSSF_status option=f75ad846" {
		t.Errorf("added %d %v: %v", n, err, wf.mutations)
	}
}

// Adding an issue that already has a Status (e.g. it was added meanwhile)
// leaves the Status alone.
func TestAddToBoardKeepsStatus(t *testing.T) {
	f, srv := newFake(t)
	var mutations []string
	f.extra = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if r.URL.Path != "/graphql" {
			return false
		}
		switch {
		case strings.Contains(string(body), "addProjectV2ItemById"):
			mutations = append(mutations, "add")
			_, _ = io.WriteString(w, `{"data":{"addProjectV2ItemById":{"item":{"id":"PVTI_9","status":{"name":"In progress"}}}}}`)
		case strings.Contains(string(body), "updateProjectV2ItemFieldValue"):
			mutations = append(mutations, "status")
			_, _ = io.WriteString(w, `{"data":{}}`)
		default:
			return false
		}
		return true
	}
	tasks := newTestTasks(t, srv)
	tasks.AddAssigned = true
	if _, err := tasks.AddToBoard(context.Background(), "A", []attendance.Task{{NodeID: "I_9", OffBoard: true}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(mutations, ",") != "add" {
		t.Errorf("status overwritten: %v", mutations)
	}
}

func TestSweepBoard(t *testing.T) {
	f, srv := newFake(t)
	wf := &writeFake{}
	writes := wf.handler(t)
	var query string
	f.extra = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if r.URL.Path == "/graphql" && strings.Contains(string(body), "first: 100, after: $after") {
			var q struct {
				Variables map[string]any `json:"variables"`
			}
			_ = json.Unmarshal(body, &q)
			query, _ = q.Variables["q"].(string)
			_, _ = io.WriteString(w, `{"data":{"search":{"issueCount":5,"pageInfo":{"hasNextPage":false},"nodes":[
 {"id":"I_1","number":1,"url":"u1","state":"OPEN","assignees":{"nodes":[{"login":"Jana-Dev"}]},"projectItems":{"nodes":[]}},
 {"id":"I_2","number":2,"url":"u2","state":"OPEN","assignees":{"nodes":[{"login":"jana-dev"}]},"projectItems":{"nodes":[{"project":{"id":"PVT_kwDOinkyank"}}]}},
 {"id":"I_3","number":3,"url":"u3","state":"OPEN","assignees":{"nodes":[]},"projectItems":{"nodes":[]}},
 {"id":"I_4","number":4,"url":"u4","state":"OPEN","assignees":{"nodes":[{"login":"stranger"}]},"projectItems":{"nodes":[]}},
 {"id":"I_5","number":5,"url":"u5","state":"OPEN","assignees":{"nodes":[{"login":"stranger"},{"login":"JANA-DEV"}]},"projectItems":{"nodes":[{"project":{"id":"PVT_other"}}]}},
 {}
]}}}`)
			return true
		}
		return writes(w, r, body)
	}
	tasks := newTestTasks(t, srv)
	tasks.AddAssigned = true
	linked := map[string]bool{"jana-dev": true}
	since := time.Date(2026, 11, 27, 10, 0, 0, 0, time.FixedZone("IST", 19800))

	seen, err := tasks.SweepBoard(context.Background(), "A", since, linked)
	// Added: #1 and #5 (assigned to a linked member, not on this Project);
	// seen but not added: #2 (already on it); ignored: #3 (unassigned),
	// #4 (not linked) and the non-issue. Assignees come lowercase.
	if err != nil || strings.Count(strings.Join(wf.mutations, ";"), "add-to-project") != 2 {
		t.Fatalf("seen %v %v: %v", seen, err, wf.mutations)
	}
	var got []string
	for _, is := range seen {
		got = append(got, fmt.Sprintf("%s:%v", strings.Join(is.Assignees, "+"), is.Added))
	}
	if strings.Join(got, " ") != "jana-dev:true jana-dev:false jana-dev:true" {
		t.Errorf("seen: %v", got)
	}
	if query != "org:inkyank-org is:issue is:open archived:false updated:>=2026-11-27T04:30:00+00:00" {
		t.Errorf("query %q", query)
	}
	// Full sweep: no updated filter.
	if _, err := tasks.SweepBoard(context.Background(), "A", time.Time{}, linked); err != nil || strings.Contains(query, "updated") {
		t.Errorf("full sweep query %q %v", query, err)
	}
}

// Two teams on one org with different Projects: the target is ambiguous,
// so nothing is added anywhere and a pick of an off-board issue is refused.
func TestAdoptionNeedsOneProjectPerOrg(t *testing.T) {
	f, srv := newFake(t)
	wf := &writeFake{resource: issueResource("Jana-Dev", "OPEN", nil, false)}
	f.extra = wf.handler(t)
	tasks := NewTasks(newTestApp(t, srv), map[string]TeamProject{
		"A": {Org: testOrg, Number: 1}, "C": {Org: strings.ToUpper(testOrg), Number: 2},
	}, quietLog())
	tasks.AddAssigned = true
	if seen, err := tasks.SweepBoard(context.Background(), "A", time.Time{}, map[string]bool{"jana-dev": true}); len(seen) != 0 || err != nil {
		t.Errorf("sweep: %v %v", seen, err)
	}
	if _, err := tasks.StartTask(context.Background(), "A", "Jana-Dev", issueURL); !errors.Is(err, attendance.ErrNotOnProject) {
		t.Errorf("pick: %v", err)
	}
	if len(wf.mutations) != 0 {
		t.Errorf("wrote: %v", wf.mutations)
	}
}

// Picking an assigned issue that isn't on the board yet adds it, then
// moves it to In progress.
func TestStartTaskAddsToBoard(t *testing.T) {
	f, srv := newFake(t)
	wf := &writeFake{resource: issueResource("Jana-Dev", "OPEN", nil, false)}
	f.extra = wf.handler(t)
	tasks := newTestTasks(t, srv)
	tasks.AddAssigned = true
	p, err := tasks.StartTask(context.Background(), "A", "Jana-Dev", issueURL)
	if err != nil || !p.Started || strings.Join(wf.mutations, ";") != "add-to-project;status item=PVTI_new field=PVTSSF_status option=47fc9ee4" {
		t.Errorf("pick: %+v %v %v", p, err, wf.mutations)
	}
	// Still refused for someone else's issue.
	wf.mutations, wf.resource = nil, issueResource("someone-else", "OPEN", nil, false)
	if _, err := tasks.StartTask(context.Background(), "A", "Jana-Dev", issueURL); !errors.Is(err, attendance.ErrNotAssigned) || len(wf.mutations) != 0 {
		t.Errorf("other's issue: %v %v", err, wf.mutations)
	}
}
