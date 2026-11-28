package attendance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// adminStore adds what the admin pages need to the pages test store.
type adminStore struct {
	*pagesStore
	decided []string
}

func (f *adminStore) SyncMembers(context.Context, []mattermost.User) error { return nil }
func (f *adminStore) AllMembers(context.Context) (map[string]MemberSettings, error) {
	return map[string]MemberSettings{
		"u":  f.member,
		"u2": {UserID: "u2", Username: "barath", Tracked: true},
		"u3": {UserID: "u3", Username: "ravi", Tracked: true},
		"u4": {UserID: "u4", Username: "kumar", Tracked: true},
		"u5": {UserID: "u5", Username: "ghost", Tracked: false},
	}, nil
}
func (f *adminStore) PendingWFH(_ context.Context, teamID, _ string) ([]WFHRequest, error) {
	var out []WFHRequest
	for _, r := range f.wfh {
		if r.TeamID == teamID && r.Status == WFHPending {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *adminStore) ListTeamWFH(_ context.Context, teamID, _ string, _ int) ([]WFHRequest, error) {
	var out []WFHRequest
	for _, r := range f.wfh {
		if r.TeamID == teamID {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *adminStore) DecideWFH(_ context.Context, id int64, teamID, status, decider, note string, at time.Time) (*WFHRequest, error) {
	for i, r := range f.wfh {
		if r.ID == id && r.TeamID == teamID {
			if r.Status != WFHPending {
				return &f.wfh[i], ErrAlreadyDecided
			}
			f.wfh[i].Status, f.wfh[i].DecidedBy, f.wfh[i].DecidedByName, f.wfh[i].Note = status, decider, "barath", note
			f.decided = append(f.decided, status)
			c := f.wfh[i]
			return &c, nil
		}
	}
	return nil, errors.New("not found")
}
func (f *adminStore) SetOpenSessionMode(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (f *adminStore) TeamSessionsBetween(context.Context, string, time.Time, time.Time) ([]ExportRow, error) {
	return nil, nil
}

type rosterOf []mattermost.User

func (r rosterOf) TeamUsers(context.Context, string) ([]mattermost.User, error) { return r, nil }

func newAdminPanel(now time.Time) (*Service, *adminStore) {
	svc, est, _, _ := newAdminEODService(now)
	st := &adminStore{pagesStore: &pagesStore{eodStore: est}}
	svc.Store = st
	svc.Roster = rosterOf{
		{ID: "u", Username: "jana"}, {ID: "u2", Username: "barath"}, {ID: "u3", Username: "ravi"},
		{ID: "u4", Username: "kumar"}, {ID: "u5", Username: "ghost"}, {ID: "bot", Username: "attendance", IsBot: true},
	}
	return svc, st
}

func TestAdminTeamDay(t *testing.T) {
	now := istAt(2026, 11, 27, 18, 0)
	svc, st := newAdminPanel(now)
	ci := func(h int) time.Time { return istAt(2026, 11, 27, h, 0) }
	co := func(h int) *time.Time { x := ci(h); return &x }
	// u (jana): checked out, report sent with a blocker. u2: still working.
	// u3: checked out, no report. u4: on break. u5: untracked, absent.
	st.sessions = []Session{{TeamID: "A", CheckIn: ci(9), CheckOut: co(17), WorkMode: "wfh"}}
	st.others = map[string][]Session{
		"u2": {{TeamID: "A", CheckIn: ci(10), WorkMode: "office"}},
		"u3": {{TeamID: "A", CheckIn: ci(9), CheckOut: co(13), WorkMode: "office"}},
		"u4": {{TeamID: "A", CheckIn: ci(9), WorkMode: "office", Breaks: []Break{{Start: ci(17)}}}},
	}
	st.reports["u/A/2026-11-27"] = &EODReport{UserID: "u", TeamID: "A", Day: "2026-11-27", SyncStatus: "synced",
		Items: []EODItem{{IssueURL: "x", NewStatus: StatusDone}, {IssueURL: "y", NewStatus: StatusBlocked}}}
	st.wfh = []WFHRequest{{ID: 3, UserID: "u3", TeamID: "A", Status: WFHPending, Start: "2026-12-01", End: "2026-12-01"}}

	if _, err := svc.AdminTeamDay(context.Background(), "u", "A", ""); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("member: %v", err)
	}
	d, err := svc.AdminTeamDay(context.Background(), "teamadmin", "A", "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]TeamMemberDay{}
	var order []string
	for _, m := range d.Members {
		got[m.Username] = m
		order = append(order, m.Username)
	}
	// Working first, then break, then out; untracked absentees aren't listed.
	if strings.Join(order, ",") != "barath,kumar,jana,ravi" {
		t.Errorf("order: %v", order)
	}
	if j := got["jana"]; j.State != "out" || j.EOD != "sent" || j.Done != 1 || j.Blocked != 1 || j.Seconds != 8*3600 || j.Sync != "synced" {
		t.Errorf("jana: %+v", j)
	}
	if b := got["barath"]; b.State != "working" || b.EOD != "waiting" || b.Since == nil || b.Seconds != 8*3600 {
		t.Errorf("barath: %+v", b)
	}
	if got["kumar"].State != "break" || got["ravi"].EOD != "missing" {
		t.Errorf("kumar %+v ravi %+v", got["kumar"], got["ravi"])
	}
	if d.Working != 1 || d.OnBreak != 1 || d.Out != 2 || d.EODSent != 1 || d.EODDue != 2 || d.WFHPending != 1 || d.DayLabel != "Fri 27 Nov" {
		t.Errorf("counts: %+v", d)
	}
}

func TestAdminMemberAndAccess(t *testing.T) {
	svc, st := newAdminPanel(istAt(2026, 11, 26, 18, 0))
	ctx := context.Background()
	st.reports["u/A/2026-11-26"] = &EODReport{UserID: "u", TeamID: "A", Day: "2026-11-26", DidToday: "Shipped the export",
		SyncStatus: "synced", UpdatedAt: istAt(2026, 11, 26, 17, 30)}

	md, err := svc.AdminMember(ctx, "teamadmin", "A", "uuuuuuuuuuuuuuuuuuuuuuuuuu", "")
	if !errors.Is(err, ErrNotTeamMember) {
		t.Errorf("non-member target: %+v %v", md, err)
	}
	// "u" isn't a valid Mattermost id in this fake: use the real check path with a member id.
	svc.People.(eodPeople).members["A/uuuuuuuuuuuuuuuuuuuuuuuuuu"] = &mattermost.TeamMember{Roles: "team_user"}
	st.reports["uuuuuuuuuuuuuuuuuuuuuuuuuu/A/2026-11-26"] = st.reports["u/A/2026-11-26"]
	st.reports["uuuuuuuuuuuuuuuuuuuuuuuuuu/A/2026-11-26"].UserID = "uuuuuuuuuuuuuuuuuuuuuuuuuu"
	md, err = svc.AdminMember(ctx, "teamadmin", "A", "uuuuuuuuuuuuuuuuuuuuuuuuuu", "")
	if err != nil || !strings.Contains(md.Report, "Shipped the export") || md.SyncLine != "GitHub: ✅ synced" || md.Tasks == nil || !md.Tasks.Linked {
		t.Errorf("member detail: %+v %v", md, err)
	}
	if _, err := svc.AdminMember(ctx, "u", "A", "uuuuuuuuuuuuuuuuuuuuuuuuuu", ""); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("member asking: %v", err)
	}
	for name, call := range map[string]func() error{
		"wfh":    func() error { _, err := svc.AdminWFH(ctx, "u", "A"); return err },
		"decide": func() error { _, err := svc.AdminDecideWFH(ctx, "u", "A", 1, true, ""); return err },
		"report": func() error { _, err := svc.AdminReport(ctx, "u", "A", "week"); return err },
		"export": func() error { _, err := svc.AdminExport(ctx, "u", "A", ""); return err },
	} {
		if err := call(); !errors.Is(err, ErrNotAdmin) {
			t.Errorf("%s as member: %v", name, err)
		}
	}
	// An admin of another team isn't an admin here.
	if _, err := svc.AdminWFH(ctx, "teamadmin", "B"); err == nil {
		t.Error("team B accepted")
	}
}

func TestAdminWFHDecide(t *testing.T) {
	svc, st := newAdminPanel(istAt(2026, 11, 27, 10, 0))
	ctx := context.Background()
	st.wfh = []WFHRequest{
		{ID: 1, UserID: "u3", Username: "ravi", TeamID: "A", Start: "2026-11-27", End: "2026-11-27", Reason: "Plumber", Status: WFHPending},
		{ID: 2, UserID: "u2", Username: "barath", TeamID: "A", Start: "2026-12-01", End: "2026-12-01", Reason: "Visa", Status: WFHPending},
		{ID: 3, UserID: "u4", Username: "kumar", TeamID: "A", Start: "2026-11-26", End: "2026-11-28", Status: WFHApproved, DecidedByName: "barath"},
		{ID: 4, UserID: "x", Username: "other", TeamID: "B", Start: "2026-12-01", End: "2026-12-01", Status: WFHPending},
	}
	w, err := svc.AdminWFH(ctx, "teamadmin", "A")
	if err != nil || len(w.Pending) != 2 || len(w.Recent) != 1 || strings.Join(w.Today, ",") != "@kumar" || !w.Pending[0].Urgent || w.Pending[1].Urgent {
		t.Fatalf("list: %+v %v", w, err)
	}

	if res, err := svc.AdminDecideWFH(ctx, "teamadmin", "A", 1, true, ""); err != nil || res.Error != "" {
		t.Fatalf("approve: %+v %v", res, err)
	}
	if res, _ := svc.AdminDecideWFH(ctx, "teamadmin", "A", 2, false, ""); res.Errors["reason"] == "" {
		t.Errorf("reject without a reason: %+v", res)
	}
	if res, err := svc.AdminDecideWFH(ctx, "teamadmin", "A", 2, false, "Client demo that day"); err != nil || res.Error != "" {
		t.Errorf("reject: %+v %v", res, err)
	}
	if res, _ := svc.AdminDecideWFH(ctx, "teamadmin", "A", 1, true, ""); !strings.Contains(res.Error, "Already approved") {
		t.Errorf("twice: %+v", res)
	}
	if res, _ := svc.AdminDecideWFH(ctx, "teamadmin", "A", 4, true, ""); res.Error != "Request not found." {
		t.Errorf("another team's request: %+v", res)
	}
	if strings.Join(st.decided, ",") != "approved,rejected" || st.wfh[1].Note != "Client demo that day" {
		t.Errorf("decided %v %+v", st.decided, st.wfh[1])
	}
}

func TestAdminReportAndExport(t *testing.T) {
	svc, st := newAdminPanel(istAt(2026, 11, 27, 10, 0))
	ctx := context.Background()
	svc.Workdays = map[time.Weekday]bool{time.Monday: true, time.Tuesday: true, time.Wednesday: true, time.Thursday: true, time.Friday: true}
	co := istAt(2026, 11, 24, 17, 0)
	st.sessions = []Session{{TeamID: "A", CheckIn: istAt(2026, 11, 24, 9, 0), CheckOut: &co, WorkMode: "wfh"}}
	r, err := svc.AdminReport(ctx, "teamadmin", "A", "week")
	if err != nil || r.Range != "week" || r.Label != "Mon 23 Nov – Fri 27 Nov" {
		t.Fatalf("report: %+v %v", r, err)
	}
	var jana *TeamReportItem
	for i := range r.Rows {
		if r.Rows[i].Username == "jana" {
			jana = &r.Rows[i]
		}
	}
	if jana == nil || jana.Days != 1 || jana.TeamSeconds != 8*3600 || jana.WFHDays != 1 {
		t.Errorf("jana: %+v (rows %+v)", jana, r.Rows)
	}

	// EOD rate: jana worked Tue (report) and Wed (none); a report on a day
	// without time doesn't count. barath is still checked in today: not due.
	wco := istAt(2026, 11, 25, 17, 0)
	st.sessions = append(st.sessions, Session{TeamID: "A", CheckIn: istAt(2026, 11, 25, 9, 0), CheckOut: &wco, WorkMode: "office"})
	st.others = map[string][]Session{"u2": {{TeamID: "A", CheckIn: istAt(2026, 11, 27, 9, 0), WorkMode: "office"}}}
	st.reports["u/A/2026-11-24"] = &EODReport{UserID: "u", TeamID: "A", Day: "2026-11-24"}
	st.reports["u/A/2026-11-23"] = &EODReport{UserID: "u", TeamID: "A", Day: "2026-11-23"}
	r, err = svc.AdminReport(ctx, "teamadmin", "A", "week")
	if err != nil {
		t.Fatal(err)
	}
	eod := map[string]string{}
	for _, row := range r.Rows {
		eod[row.Username] = fmt.Sprintf("%d/%d", row.EODSent, row.EODDue)
	}
	if eod["jana"] != "1/2" || eod["barath"] != "0/0" || r.EODSent != 1 || r.EODDue != 2 {
		t.Errorf("EOD rate: %v total %d/%d", eod, r.EODSent, r.EODDue)
	}
	if _, err := svc.AdminReport(ctx, "teamadmin", "A", "year"); !errors.Is(err, ErrBadRange) {
		t.Errorf("bad range: %v", err)
	}
	if msg, err := svc.AdminExport(ctx, "teamadmin", "A", ""); err != nil || msg == "" {
		t.Errorf("export: %q %v", msg, err)
	}
}
