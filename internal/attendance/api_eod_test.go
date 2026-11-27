package attendance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// verify parses a signed state like the handler does.
func verifyState(t *testing.T, svc *Service, state string) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal([]byte(state), &m); err != nil {
		t.Fatal(err)
	}
	f, err := svc.Signer.(*mattermost.Signer).Verify(m)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPanelEODForm(t *testing.T) {
	svc, st, p, ft := newAdminEODService(istAt(2026, 11, 26, 18, 0))
	ft.eodOpen = []Task{task(45, StatusInProgress, 9), task(47, StatusTodo, 8)}
	ft.eodClosed = []Task{task(42, StatusDone, 11)}
	var got notes
	svc.Notify = got.record
	ctx := context.Background()

	f, err := svc.PanelEOD(ctx, "u", "A", "")
	if err != nil || !f.Available || f.Day != "2026-11-26" || f.DayLabel != "Thu 26 Nov" || f.Submitted {
		t.Fatalf("form: %+v %v", f, err)
	}
	// Same rows as the dialog: #45 (In progress) gets a status row, #47
	// (Todo) is under "other tasks", #42 closed today is already done.
	if len(f.Tasks) != 1 || f.Tasks[0].Number != 45 || f.Tasks[0].Choice != noChange || f.Tasks[0].Title != "Task 45" ||
		len(f.Others) != 1 || f.Others[0].Number != 47 || len(f.Done) != 1 || f.Done[0].Number != 42 {
		t.Fatalf("rows: %+v / %+v / %+v", f.Tasks, f.Others, f.Done)
	}
	if strings.Join(f.Statuses, ",") != "In progress,In review,Blocked,Done" || f.MinDid != minDidLen {
		t.Errorf("limits: %+v", f)
	}

	// Submit through the panel: saved, posted in the thread, panel told.
	fields := verifyState(t, svc, f.State)
	res, err := svc.PanelSubmitEOD(ctx, "u", "A", fields, map[string]any{
		"s0": StatusBlocked, "others": []any{"0"}, "did": "Finished the timeout work", "blockers": "Waiting for review",
	})
	if err != nil || res.Error != "" || len(res.Errors) > 0 {
		t.Fatalf("submit: %+v %v", res, err)
	}
	r := st.reports["u/A/2026-11-26"]
	if r == nil || r.Blockers != "Waiting for review" || len(r.Items) != 3 || len(p.replies) != 1 {
		t.Fatalf("report: %+v replies %d", r, len(p.replies))
	}
	if !strings.Contains(strings.Join(got, ","), "u:eod_changed:A") {
		t.Errorf("no eod_changed: %v", got)
	}

	// Reopening pre-fills the saved report.
	f, _ = svc.PanelEOD(ctx, "u", "A", "")
	if !f.Submitted || f.SubmittedAt == nil || f.Did != "Finished the timeout work" || f.Blockers != "Waiting for review" ||
		f.Tasks[0].Choice != StatusBlocked || !f.Others[0].Checked || f.SyncLine == "" {
		t.Errorf("prefill: %+v", f)
	}

	// Validation is the dialog's: too short.
	res, _ = svc.PanelSubmitEOD(ctx, "u", "A", verifyState(t, svc, f.State), map[string]any{"did": "short"})
	if res.Errors["did"] == "" {
		t.Errorf("short text accepted: %+v", res)
	}
	// Someone else's form, or another team's, is refused.
	other := verifyState(t, svc, f.State)
	other["u"] = "someone"
	if res, _ := svc.PanelSubmitEOD(ctx, "u", "A", other, map[string]any{"did": "Finished the timeout work"}); res.Error == "" {
		t.Error("another user's form accepted")
	}
	other = verifyState(t, svc, f.State)
	other["t"] = "B"
	if res, _ := svc.PanelSubmitEOD(ctx, "u", "A", other, map[string]any{"did": "Finished the timeout work"}); res.Error == "" {
		t.Error("another team's form accepted")
	}
}

func TestPanelEODUnavailable(t *testing.T) {
	svc, st, _, _ := newAdminEODService(istAt(2026, 11, 26, 18, 0))
	ctx := context.Background()
	st.sessions = nil // didn't work today
	if f, err := svc.PanelEOD(ctx, "u", "A", ""); err != nil || f.Available || !strings.Contains(f.Message, "nothing to report yet") {
		t.Errorf("not worked: %+v %v", f, err)
	}
	// A day outside the window (yesterday after 11:00, or older) is closed.
	svc.Now = func() time.Time { return istAt(2026, 11, 27, 12, 0) }
	if f, _ := svc.PanelEOD(ctx, "u", "A", "2026-11-26"); f.Available || !strings.Contains(f.Message, "is closed") {
		t.Errorf("closed day: %+v", f)
	}
}
