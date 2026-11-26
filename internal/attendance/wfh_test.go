package attendance

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

func TestValidateWFH(t *testing.T) {
	now := istAt(2026, 11, 25, 15, 0)
	s, e, errs := ValidateWFH("2026-11-27", "", "plumber visit", now, ist)
	if len(errs) != 0 || !s.Equal(e) || s.Format(DayFormat) != "2026-11-27" {
		t.Fatalf("single day: %v %v %v", s, e, errs)
	}
	if _, _, errs := ValidateWFH("2026-11-25", "2026-11-26", "ok reason", now, ist); len(errs) != 0 {
		t.Errorf("today should be allowed: %v", errs)
	}
	cases := map[string][3]string{
		"start_date": {"2026-11-24", "", "past"},
		"end_date":   {"2026-11-27", "2026-11-26", "end before start"},
	}
	for field, c := range cases {
		if _, _, errs := ValidateWFH(c[0], c[1], c[2], now, ist); errs[field] == "" {
			t.Errorf("%s: expected error on %s, got %v", c[2], field, errs)
		}
	}
	if _, _, errs := ValidateWFH("27/11/2026", "", "reason", now, ist); errs["start_date"] == "" {
		t.Error("bad format accepted")
	}
	if _, _, errs := ValidateWFH("2026-12-01", "2027-01-15", "long trip", now, ist); errs["end_date"] == "" {
		t.Error("over-long range accepted")
	}
	if _, _, errs := ValidateWFH("2026-12-01", "", " ", now, ist); errs["reason"] == "" {
		t.Error("empty reason accepted")
	}
}

func TestIsUrgent(t *testing.T) {
	today, tomorrow, later := istAt(2026, 11, 25, 0, 0), istAt(2026, 11, 26, 0, 0), istAt(2026, 11, 27, 0, 0)
	if !IsUrgent(today, istAt(2026, 11, 25, 9, 0), ist) {
		t.Error("same day should be urgent")
	}
	if IsUrgent(tomorrow, istAt(2026, 11, 25, 19, 59), ist) || !IsUrgent(tomorrow, istAt(2026, 11, 25, 20, 0), ist) {
		t.Error("tomorrow is urgent only after 20:00")
	}
	if IsUrgent(later, istAt(2026, 11, 25, 23, 0), ist) {
		t.Error("day after tomorrow isn't urgent")
	}
}

func TestExpectedPeriod(t *testing.T) {
	p := week(2026, 11, 23)                                             // Mon 23 – Sun 29
	since := istAt(2026, 11, 25, 12, 0)                                 // first seen Wed
	ep, ok := ExpectedPeriod(p, since, istAt(2026, 11, 27, 10, 0), ist) // Fri morning
	if !ok || ep.FirstDay.Format(DayFormat) != "2026-11-25" || ep.LastDay.Format(DayFormat) != "2026-11-26" {
		t.Fatalf("got %v – %v ok=%v", ep.FirstDay, ep.LastDay, ok)
	}
	if got := expectedFor(8, p, "A", monFri, nil, since, istAt(2026, 11, 27, 10, 0), ist); got != 16*time.Hour {
		t.Errorf("expected Wed+Thu = 16h, got %v", got)
	}
	if _, ok := ExpectedPeriod(p, since, istAt(2026, 11, 25, 18, 0), ist); ok {
		t.Error("first day itself isn't over yet: nothing expected")
	}
	full, _ := ExpectedPeriod(p, time.Time{}, istAt(2026, 12, 1, 9, 0), ist)
	if full.FirstDay != p.FirstDay || full.LastDay != p.LastDay {
		t.Error("past week with unknown since should be unchanged")
	}
}

// --- service-level tests with a partial fake store ---

// wfhStore embeds Store so unused methods panic if called.
type wfhStore struct {
	Store
	approvedOn   bool
	insertedMode string
	decideErr    error
	req          WFHRequest
	decided      int
	modeChanged  bool
	audits       int
}

func (f *wfhStore) MemberRefreshedAt(context.Context, string) (time.Time, bool, error) {
	return time.Now(), true, nil
}
func (f *wfhStore) WFHApprovedOn(context.Context, string, string, string) (bool, error) {
	return f.approvedOn, nil
}
func (f *wfhStore) InsertSession(_ context.Context, _, _ string, _ time.Time, mode, _ string) (int64, error) {
	f.insertedMode = mode
	return 1, nil
}
func (f *wfhStore) GetWFH(context.Context, int64) (*WFHRequest, error) {
	r := f.req
	return &r, nil
}
func (f *wfhStore) DecideWFH(_ context.Context, _ int64, _, status, decider, note string, at time.Time) (*WFHRequest, error) {
	r := f.req
	if f.decideErr != nil {
		r.Status, r.DecidedByName = WFHApproved, "janarthanan"
		return &r, f.decideErr
	}
	f.decided++
	r.Status, r.DecidedBy, r.DecidedByName, r.DecidedAt, r.Note = status, decider, "barath", &at, note
	return &r, nil
}
func (f *wfhStore) ApprovedWFHDays(context.Context, string, string, string, string) (float64, error) {
	return 1, nil
}
func (f *wfhStore) InsertAudit(context.Context, AuditEntry) error { f.audits++; return nil }
func (f *wfhStore) SetOpenSessionMode(context.Context, string, string, string) (bool, error) {
	return f.modeChanged, nil
}

type fakePoster struct{ dms []string }

func (p *fakePoster) CreatePostWithButtons(context.Context, string, string, []mattermost.Attachment) (string, error) {
	return "post1", nil
}
func (p *fakePoster) UpdatePost(context.Context, string, string, []mattermost.Attachment) error {
	return nil
}
func (p *fakePoster) EphemeralPost(context.Context, string, string, string) error { return nil }
func (p *fakePoster) CreateReply(context.Context, string, string, string) (string, error) {
	return "reply1", nil
}
func (p *fakePoster) Permalink(_ context.Context, team, post string) (string, error) {
	return "https://chat.example/" + team + "/pl/" + post, nil
}
func (p *fakePoster) DirectMessage(_ context.Context, _ string, msg string) error {
	p.dms = append(p.dms, msg)
	return nil
}
func (p *fakePoster) OpenDialog(context.Context, string, string, mattermost.Dialog) error { return nil }

func newWFHService(st *wfhStore) (*Service, *fakePoster) {
	posts := &fakePoster{}
	return &Service{
		Store: st, Teams: teamNames{"A": "InkYank", "B": "Seechanlabs"},
		Signer: mattermost.NewSigner("k", time.Hour), ActionURL: "http://x/mm/action",
		Admins: NewAdminChecker(roles(), time.Minute), Posts: posts,
		Loc: ist, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return istAt(2026, 11, 25, 10, 0) },
	}, posts
}

func TestCheckInModeFollowsApprovedWFH(t *testing.T) {
	for _, approved := range []bool{true, false} {
		st := &wfhStore{approvedOn: approved}
		svc, _ := newWFHService(st)
		r := svc.Handle(context.Background(), Command{TeamID: "A", UserID: "u", Text: "in"})
		want := map[bool]string{true: "wfh", false: "office"}[approved]
		if st.insertedMode != want {
			t.Errorf("approved=%v: mode %q, want %q (%s)", approved, st.insertedMode, want, r.Text)
		}
		if approved && !strings.Contains(r.Text, "🏠") {
			t.Errorf("reply should show 🏠: %s", r.Text)
		}
	}
}

func wfhClick(action, user, team string) Click {
	return Click{Action: action, UserID: user, Fields: map[string]string{"a": action, "w": "12", "t": team}}
}

func pendingReq() WFHRequest {
	return WFHRequest{ID: 12, UserID: "priya", Username: "priya", TeamID: "A", Start: "2026-11-25", End: "2026-11-25", Reason: "plumber", Status: WFHPending}
}

func TestNonAdminCannotApprove(t *testing.T) {
	st := &wfhStore{req: pendingReq()}
	svc, _ := newWFHService(st)
	for _, c := range []Click{
		wfhClick(ActWFHApprove, "plain", "A"),
		wfhClick(ActWFHApprove, "teamadmin", "B"), // admin of A, not B
	} {
		st.req.TeamID = c.Fields["t"]
		r := svc.HandleButton(context.Background(), c)
		if !r.EphemeralOnly || !strings.Contains(r.Text, "Only admins") || st.decided != 0 {
			t.Errorf("%s in %s: %+v decided=%d", c.UserID, c.Fields["t"], r, st.decided)
		}
	}
}

func TestApproveSameDayUpdatesOpenSessionAndDMs(t *testing.T) {
	st := &wfhStore{req: pendingReq(), modeChanged: true}
	svc, posts := newWFHService(st)
	st.Store = &openSessionOnly{}
	r := svc.HandleButton(context.Background(), wfhClick(ActWFHApprove, "teamadmin", "A"))
	if r.EphemeralOnly || !strings.Contains(r.Text, "✅ **Approved** by @barath") || r.Attachments != nil {
		t.Fatalf("post update: %+v", r)
	}
	if st.decided != 1 || st.audits != 1 || len(posts.dms) != 1 || !strings.Contains(posts.dms[0], "approved") {
		t.Errorf("decided=%d audits=%d dms=%v", st.decided, st.audits, posts.dms)
	}
}

// openSessionOnly answers OpenSession for the status update after approval.
type openSessionOnly struct{ Store }

func (openSessionOnly) OpenSession(context.Context, string) (*Session, error) {
	return &Session{TeamID: "A", CheckIn: istAt(2026, 11, 25, 9, 0)}, nil
}

func TestSecondApproverSeesAlreadyDecided(t *testing.T) {
	st := &wfhStore{req: pendingReq(), decideErr: ErrAlreadyDecided}
	svc, posts := newWFHService(st)
	r := svc.HandleButton(context.Background(), wfhClick(ActWFHApprove, "sys", "A"))
	if !r.EphemeralOnly || !strings.Contains(r.Text, "Already approved by @janarthanan") || len(posts.dms) != 0 || st.audits != 0 {
		t.Fatalf("got %+v dms=%v audits=%d", r, posts.dms, st.audits)
	}
}

func TestPendingWFHReminderListsTomorrowOnce(t *testing.T) {
	st := newFakeJobStore()
	st.wfh = []WFHRequest{
		{ID: 1, Username: "priya", TeamID: "A", Start: "2026-11-26", End: "2026-11-26", Reason: "x", Status: WFHPending},
		{ID: 2, Username: "raj", TeamID: "A", Start: "2026-11-28", End: "2026-11-28", Reason: "y", Status: WFHPending},
		{ID: 3, Username: "kiran", TeamID: "A", Start: "2026-11-24", End: "2026-11-27", Reason: "z", Status: WFHApproved},
	}
	j, msg := newJobs(st, nil)
	now := istAt(2026, 11, 25, 18, 0)
	j.PendingWFHReminder(context.Background(), now)
	j.PendingWFHReminder(context.Background(), now)
	if len(msg.posts["admA"]) != 1 || len(msg.posts["admB"]) != 0 {
		t.Fatalf("posts: %v", msg.posts)
	}
	p := msg.posts["admA"][0]
	if !strings.Contains(p, "@priya") || strings.Contains(p, "@raj") || strings.Contains(p, "@kiran") {
		t.Errorf("wrong list:\n%s", p)
	}
}

func TestWFHAllTable(t *testing.T) {
	now := istAt(2026, 11, 25, 11, 0)
	at := istAt(2026, 11, 24, 16, 0)
	rs := []WFHRequest{
		{ID: 1, Username: "priya", Start: "2026-11-24", End: "2026-11-26", Status: WFHApproved, DecidedByName: "barath", DecidedAt: &at, Reason: "plumber"},
		{ID: 2, Username: "raj", Start: "2026-11-25", End: "2026-11-25", HalfDay: true, Status: WFHApproved, DecidedByName: "barath", Reason: "doctor"},
		{ID: 3, Username: "kiran", Start: "2026-11-25", End: "2026-11-25", Status: WFHRejected, DecidedByName: "madmap", Note: "client demo", Reason: "x"},
		{ID: 4, Username: "jana", Start: "2026-11-26", End: "2026-11-26", Status: WFHPending, Reason: "y"},
	}
	out := WFHAllTable("InkYank", rs, now, ist)
	for _, want := range []string{
		"🏠 **WFH today:** @priya, @raj (½)",
		"| 1 | @priya | Tue 24 Nov – Thu 26 Nov (3 days) | ✅ approved | @barath | plumber |",
		"| 3 | @kiran | Wed 25 Nov (full day) | ❌ rejected | @madmap: client demo | x |",
		"| 4 | @jana | Thu 26 Nov (full day) | ⏳ pending |  | y |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "@kiran,") {
		t.Error("rejected request counted as WFH today")
	}
}
