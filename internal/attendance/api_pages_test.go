package attendance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func (f *eodStore) HolidaysBetween(context.Context, string, string) ([]Holiday, error) {
	return f.holidays, nil
}

// pagesStore adds what the WFH page needs to the EOD test store.
type pagesStore struct {
	*eodStore
	wfh      []WFHRequest
	nextWFH  int64
	audits   int
	adminPst string
}

func (f *pagesStore) ListWFH(_ context.Context, userID, teamID string, limit int) ([]WFHRequest, error) {
	var out []WFHRequest
	for i := len(f.wfh) - 1; i >= 0 && len(out) < limit; i-- {
		if f.wfh[i].UserID == userID && f.wfh[i].TeamID == teamID {
			out = append(out, f.wfh[i])
		}
	}
	return out, nil
}
func (f *pagesStore) CreateWFH(_ context.Context, r WFHRequest) (int64, error) {
	for _, o := range f.wfh {
		if o.UserID == r.UserID && o.TeamID == r.TeamID && (o.Status == WFHPending || o.Status == WFHApproved) && o.Start <= r.End && r.Start <= o.End {
			return 0, ErrWFHOverlap
		}
	}
	f.nextWFH++
	r.ID = f.nextWFH
	f.wfh = append(f.wfh, r)
	return r.ID, nil
}
func (f *pagesStore) GetWFH(_ context.Context, id int64) (*WFHRequest, error) {
	for _, r := range f.wfh {
		if r.ID == id {
			c := r
			return &c, nil
		}
	}
	return nil, nil
}
func (f *pagesStore) CancelWFH(_ context.Context, id int64, userID, today string) (*WFHRequest, error) {
	for i, r := range f.wfh {
		if r.ID == id && r.UserID == userID && (r.Status == WFHPending || (r.Status == WFHApproved && r.Start > today)) {
			f.wfh[i].Status = WFHCancelled
			c := f.wfh[i]
			return &c, nil
		}
	}
	return nil, nil
}
func (f *pagesStore) ApprovedWFHDays(context.Context, string, string, string, string) (float64, error) {
	return 0, nil
}
func (f *pagesStore) SetWFHAdminPost(_ context.Context, _ int64, post string) error {
	f.adminPst = post
	return nil
}
func (f *pagesStore) InsertAudit(context.Context, AuditEntry) error { f.audits++; return nil }

func TestPanelHoursWeek(t *testing.T) {
	// Fri 27 Nov, 15:00. Worked Mon 23 (8h office), Wed 25 (7h WFH), today
	// since 09:00 (still checked in), plus 2h for another org on Tue.
	svc, st, _, _ := newAdminEODService(istAt(2026, 11, 27, 15, 0))
	out := func(y, d, h int) *time.Time { t := istAt(y, 11, d, h, 0); return &t }
	st.sessions = []Session{
		{TeamID: "A", CheckIn: istAt(2026, 11, 23, 9, 0), CheckOut: out(2026, 23, 17), WorkMode: "office"},
		{TeamID: "B", CheckIn: istAt(2026, 11, 24, 10, 0), CheckOut: out(2026, 24, 12), WorkMode: "office"},
		{TeamID: "A", CheckIn: istAt(2026, 11, 25, 9, 0), CheckOut: out(2026, 25, 16), WorkMode: "wfh"},
		{TeamID: "A", CheckIn: istAt(2026, 11, 27, 9, 0), WorkMode: "office"},
	}
	st.member.ExpectedHours, st.member.Tracked = 8, true
	svc.Workdays = map[time.Weekday]bool{time.Monday: true, time.Tuesday: true, time.Wednesday: true, time.Thursday: true, time.Friday: true}
	st.holidays = []Holiday{{Day: "2026-11-26", Name: "Festival"}}

	h, err := svc.PanelHours(context.Background(), "u", "A", "week")
	if err != nil {
		t.Fatal(err)
	}
	if h.Range != "week" || h.Label != "Mon 23 Nov – Fri 27 Nov" || len(h.Days) != 5 {
		t.Fatalf("range %q %q days %d", h.Range, h.Label, len(h.Days))
	}
	mon, wed, fri := h.Days[0], h.Days[2], h.Days[4]
	if mon.Seconds != 8*3600 || strings.Join(mon.Modes, ",") != "office" || wed.Seconds != 7*3600 || wed.Modes[0] != "wfh" ||
		fri.Seconds != 6*3600 || !fri.Open || h.Days[1].Seconds != 0 {
		t.Errorf("days: %+v", h.Days)
	}
	if h.TeamSeconds != 21*3600 || h.AllSeconds != 23*3600 || len(h.ByOrg) != 2 || h.ByOrg[0].Team.Name != "InkYank" || h.ByOrg[0].Seconds != 21*3600 {
		t.Errorf("totals: team %d all %d orgs %+v", h.TeamSeconds, h.AllSeconds, h.ByOrg)
	}
	// Expected: Mon–Thu (up to yesterday) minus Thursday's holiday = 3 × 8h;
	// worked in those days, all orgs: 8 + 2 + 7 = 17h.
	if !h.HasExpected || h.ExpectedSeconds != 24*3600 || h.ExpectedUntil != "Thu 26 Nov" || h.WorkedUntilYesterday != 17*3600 {
		t.Errorf("expected: %+v", h)
	}

	if _, err := svc.PanelHours(context.Background(), "u", "A", "year"); !errors.Is(err, ErrBadRange) {
		t.Errorf("bad range: %v", err)
	}
	// Today: nothing finished yet, so no expected hours; untracked: none either.
	if h, _ := svc.PanelHours(context.Background(), "u", "A", "today"); h.HasExpected || h.TeamSeconds != 6*3600 || len(h.Days) != 1 {
		t.Errorf("today: %+v", h)
	}
	st.member.Tracked = false
	if h, _ := svc.PanelHours(context.Background(), "u", "A", "week"); h.HasExpected {
		t.Error("untracked member has expected hours")
	}
}

func TestPanelWFH(t *testing.T) {
	svc, est, p, _ := newAdminEODService(istAt(2026, 11, 27, 10, 0))
	st := &pagesStore{eodStore: est}
	svc.Store = st
	team := svc.TeamCfg["A"]
	team.AdminChannelID = "adminA"
	svc.TeamCfg["A"] = team
	var got notes
	svc.Notify = got.record
	ctx := context.Background()

	// A request from the panel: same validation and admin post as /att wfh.
	res, err := svc.PanelRequestWFH(ctx, "u", "A", "2026-12-01", "", false, "Plumber visit")
	if err != nil || res.Error != "" || len(res.Errors) > 0 {
		t.Fatalf("request: %+v %v", res, err)
	}
	if len(st.wfh) != 1 || st.wfh[0].Start != "2026-12-01" || st.wfh[0].End != "2026-12-01" || st.adminPst == "" || len(p.ephemeral) != 0 {
		t.Errorf("stored %+v admin post %q ephemeral %v", st.wfh, st.adminPst, p.ephemeral)
	}
	if strings.Join(got, ",") != "u:wfh_changed:A" {
		t.Errorf("notified %v", got)
	}
	// Overlap and validation errors come back per field.
	if res, _ := svc.PanelRequestWFH(ctx, "u", "A", "2026-12-01", "", false, "Again"); res.Errors["start_date"] == "" {
		t.Errorf("overlap: %+v", res)
	}
	if res, _ := svc.PanelRequestWFH(ctx, "u", "A", "2026-11-20", "", false, "Past"); len(res.Errors) == 0 {
		t.Errorf("past date accepted: %+v", res)
	}

	st.wfh = append(st.wfh,
		WFHRequest{ID: 7, UserID: "u", TeamID: "A", Start: "2026-11-20", End: "2026-11-20", Status: WFHApproved, DecidedByName: "barath"},
		WFHRequest{ID: 8, UserID: "u", TeamID: "A", Start: "2026-11-25", End: "2026-11-25", Status: WFHRejected, DecidedByName: "barath", Note: "client demo"},
		WFHRequest{ID: 9, UserID: "u", TeamID: "A", Start: "2026-12-03", End: "2026-12-04", Status: WFHApproved, DecidedByName: "barath"},
		WFHRequest{ID: 10, UserID: "someone", TeamID: "A", Start: "2026-12-03", End: "2026-12-03", Status: WFHPending},
	)
	w, err := svc.PanelWFH(ctx, "u", "A")
	if err != nil || len(w.Requests) != 4 || w.Pending != 1 || w.Today != "2026-11-27" {
		t.Fatalf("list: %+v %v", w, err)
	}
	can := map[int64]bool{}
	for _, it := range w.Requests {
		can[it.ID] = it.CanCancel
	}
	// Pending and approved-not-started can be cancelled; past or decided-against can't.
	if !can[1] || can[7] || can[8] || !can[9] {
		t.Errorf("can cancel: %v", can)
	}

	msg, err := svc.PanelCancelWFH(ctx, "u", "A", 9)
	if err != nil || !strings.Contains(msg, "cancelled") {
		t.Errorf("cancel: %q %v", msg, err)
	}
	if msg, _ := svc.PanelCancelWFH(ctx, "u", "A", 10); !strings.Contains(msg, "can't be cancelled") {
		t.Errorf("someone else's: %q", msg)
	}
}

func TestPanelSettingsAndHomeTile(t *testing.T) {
	svc, _, _, _ := newAdminEODService(istAt(2026, 11, 26, 18, 0))
	ctx := context.Background()
	s, err := svc.PanelSettings(ctx, "u", "A")
	if err != nil || !s.GitHubSetUp || s.GitHubOrg != "inkyank-org" || s.GitHubLogin != "Jana-Dev" || s.Team.Name != "InkYank" {
		t.Errorf("settings: %+v %v", s, err)
	}

	// Home tile: worked today, report not sent → available, not submitted.
	me, _ := svc.Me(ctx, "u", "A")
	if !me.EOD.Available || me.EOD.Submitted || me.EOD.DayLabel != "Thu 26 Nov" || me.GitHubLogin != "Jana-Dev" || !me.GitHubSetUp || me.DisplayName != "Jana" {
		t.Errorf("me: %+v", me)
	}
}
