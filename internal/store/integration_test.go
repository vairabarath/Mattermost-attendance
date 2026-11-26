package store

// Integration tests against a real Postgres. They run only when
// TEST_DATABASE_URL is set (see scripts/test-integration.sh and the GitHub
// Actions workflow) and are skipped otherwise. Every test gets its own
// throwaway schema with the real migrations applied, so they never touch
// existing data and can run against any database you can create schemas in.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
	"github.com/vairabarath/Mattermost-attendance/migrations"
)

var ist = func() *time.Location {
	l, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		panic(err)
	}
	return l
}()

func at(y int, m time.Month, d, hh, mm int) time.Time { return time.Date(y, m, d, hh, mm, 0, 0, ist) }

const (
	u1    = "user1user1user1user1user1u"
	u2    = "user2user2user2user2user2u"
	admin = "adminadminadminadminadmina"
	teamA = "teamAteamAteamAteamAteamA1"
	teamB = "teamBteamBteamBteamBteamB1"
)

// newTestStore creates a fresh schema, migrates it and registers members.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "it_" + hex.EncodeToString(b)

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = conn.Close(context.Background())
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := &Store{Pool: pool}
	if err := s.Migrate(ctx, migrations.FS, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for id, name := range map[string]string{u1: "priya", u2: "raj", admin: "barath"} {
		if err := s.UpsertMember(ctx, id, name, ""); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// closedSession inserts and closes a session, optionally with breaks [start,end] pairs.
func closedSession(t *testing.T, s *Store, user, team string, in, out time.Time, breaks ...time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.InsertSession(ctx, user, team, in, "office", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(breaks); i += 2 {
		if _, err := s.StartBreak(ctx, user, breaks[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EndBreak(ctx, user, breaks[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CloseOpenSession(ctx, user, out, ""); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, s *Store, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIntegrationDoubleCheckInAcrossTeams(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	if _, err := s.InsertSession(ctx, u1, teamA, at(2026, 11, 25, 9, 0), "office", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertSession(ctx, u1, teamA, at(2026, 11, 25, 9, 5), "office", ""); !errors.Is(err, attendance.ErrAlreadyCheckedIn) {
		t.Errorf("same team: %v", err)
	}
	if _, err := s.InsertSession(ctx, u1, teamB, at(2026, 11, 25, 9, 5), "office", ""); !errors.Is(err, attendance.ErrAlreadyCheckedIn) {
		t.Errorf("other team: %v", err)
	}
	if _, err := s.InsertSession(ctx, u2, teamA, at(2026, 11, 25, 9, 5), "office", ""); err != nil {
		t.Errorf("another member should be able to check in: %v", err)
	}
}

func TestIntegrationBreakRules(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	if _, err := s.StartBreak(ctx, u1, at(2026, 11, 25, 9, 0)); !errors.Is(err, attendance.ErrNotCheckedIn) {
		t.Errorf("break without session: %v", err)
	}
	if _, err := s.InsertSession(ctx, u1, teamA, at(2026, 11, 25, 9, 0), "office", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EndBreak(ctx, u1, at(2026, 11, 25, 9, 30)); !errors.Is(err, attendance.ErrNotOnBreak) {
		t.Errorf("back without break: %v", err)
	}
	if _, err := s.StartBreak(ctx, u1, at(2026, 11, 25, 12, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartBreak(ctx, u1, at(2026, 11, 25, 12, 5)); !errors.Is(err, attendance.ErrAlreadyOnBreak) {
		t.Errorf("double break: %v", err)
	}
	// Checking out while on break closes the break at check-out time.
	sess, err := s.CloseOpenSession(ctx, u1, at(2026, 11, 25, 12, 30), "done")
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Breaks) != 1 || sess.Breaks[0].End == nil || !sess.Breaks[0].End.Equal(at(2026, 11, 25, 12, 30)) {
		t.Fatalf("open break not closed at check-out: %+v", sess.Breaks)
	}
	net := attendance.NetByDay([]attendance.Session{*sess}, time.Now(), ist)
	if got := net[attendance.DayKey{Day: "2026-11-25", TeamID: teamA}]; got != 3*time.Hour {
		t.Errorf("net = %v, want 3h", got)
	}
}

func TestIntegrationHoursAcrossMidnight(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	// 22:00 → 02:00 with a break 23:30–00:30 that crosses midnight.
	closedSession(t, s, u1, teamA, at(2026, 11, 24, 22, 0), at(2026, 11, 25, 2, 0),
		at(2026, 11, 24, 23, 30), at(2026, 11, 25, 0, 30))
	sessions, err := s.SessionsOverlapping(ctx, u1, at(2026, 11, 24, 0, 0), at(2026, 11, 26, 0, 0))
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions: %v %+v", err, sessions)
	}
	net := attendance.NetByDay(sessions, time.Now(), ist)
	if d1, d2 := net[attendance.DayKey{Day: "2026-11-24", TeamID: teamA}], net[attendance.DayKey{Day: "2026-11-25", TeamID: teamA}]; d1 != 90*time.Minute || d2 != 90*time.Minute {
		t.Errorf("split = %v / %v, want 1h30m / 1h30m", d1, d2)
	}
}

func TestIntegrationFixDay(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	// Two sessions in team A on the 24th (one with a break), one in team B
	// the same day, one in team A on the 23rd.
	closedSession(t, s, u1, teamA, at(2026, 11, 24, 9, 0), at(2026, 11, 24, 12, 0), at(2026, 11, 24, 10, 0), at(2026, 11, 24, 10, 15))
	closedSession(t, s, u1, teamA, at(2026, 11, 24, 13, 0), at(2026, 11, 24, 15, 0))
	closedSession(t, s, u1, teamB, at(2026, 11, 24, 16, 0), at(2026, 11, 24, 18, 0))
	closedSession(t, s, u1, teamA, at(2026, 11, 23, 9, 0), at(2026, 11, 23, 17, 0))
	start, end := attendance.DayBounds(at(2026, 11, 24, 12, 0), ist)
	req := attendance.FixRequest{
		ActorID: admin, UserID: u1, TeamID: teamA, DayStart: start, DayEnd: end,
		In: at(2026, 11, 24, 9, 30), Out: at(2026, 11, 24, 15, 30), Mode: "office", Reason: "forgot",
	}

	// Overlaps the team B session (16:00) → refused, nothing changed.
	bad := req
	bad.Out = at(2026, 11, 24, 16, 30)
	if _, err := s.FixDay(ctx, bad); !errors.Is(err, attendance.ErrFixOverlap) {
		t.Fatalf("overlap: %v", err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM sessions WHERE mm_user_id = $1`, u1); n != 4 {
		t.Fatalf("refused fix changed data: %d sessions", n)
	}

	res, err := s.FixDay(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Old) != 2 || len(res.Old[0].Breaks) != 1 {
		t.Fatalf("replaced: %+v", res.Old)
	}
	if n := countRows(t, s, `SELECT count(*) FROM sessions WHERE mm_user_id = $1 AND team_id = $2`, u1, teamB); n != 1 {
		t.Error("team B session touched")
	}
	if n := countRows(t, s, `SELECT count(*) FROM sessions WHERE mm_user_id = $1 AND team_id = $2 AND check_in < $3`, u1, teamA, start); n != 1 {
		t.Error("previous day touched")
	}
	if n := countRows(t, s, `SELECT count(*) FROM breaks`); n != 0 {
		t.Errorf("breaks of replaced sessions remain: %d", n)
	}
	var oldJSON []byte
	var action, reason string
	if err := s.Pool.QueryRow(ctx, `SELECT action, reason, old_value FROM audit_log WHERE target_id = $1`, u1).Scan(&action, &reason, &oldJSON); err != nil {
		t.Fatal(err)
	}
	var old []map[string]any
	_ = json.Unmarshal(oldJSON, &old)
	if action != "fix" || reason != "forgot" || len(old) != 2 || old[0]["breaks"] == nil {
		t.Errorf("audit: %s %s %s", action, reason, oldJSON)
	}

	// An open session in team A that started that day blocks the fix.
	if _, err := s.InsertSession(ctx, u2, teamA, at(2026, 11, 24, 9, 0), "office", ""); err != nil {
		t.Fatal(err)
	}
	open := req
	open.UserID = u2
	if _, err := s.FixDay(ctx, open); !errors.Is(err, attendance.ErrOpenSessionOnDay) {
		t.Errorf("open session: %v", err)
	}
}

func TestIntegrationAutoClose(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	in := at(2026, 11, 24, 9, 0)
	if _, err := s.InsertSession(ctx, u1, teamA, in, "office", ""); err != nil {
		t.Fatal(err)
	}
	// Lunch 13:00–14:00, then an open break from 20:00 that never ended.
	_, _ = s.StartBreak(ctx, u1, at(2026, 11, 24, 13, 0))
	_, _ = s.EndBreak(ctx, u1, at(2026, 11, 24, 14, 0))
	_, _ = s.StartBreak(ctx, u1, at(2026, 11, 24, 20, 0))
	now := at(2026, 11, 25, 3, 5)

	stale, err := s.OpenSessions(ctx, now.Add(-16*time.Hour))
	if err != nil || len(stale) != 1 || stale[0].Expected != 8 || len(stale[0].Session.Breaks) != 2 {
		t.Fatalf("stale: %v %+v", err, stale)
	}
	out := attendance.AutoCloseTime(stale[0].Session, 8*time.Hour, now)
	closedAt, closed, err := s.AutoCloseSession(ctx, stale[0].Session.ID, out)
	if err != nil || !closed || !closedAt.Equal(at(2026, 11, 24, 18, 0)) {
		t.Fatalf("close: %v %v %v", closedAt.In(ist), closed, err)
	}
	if _, closed, _ := s.AutoCloseSession(ctx, stale[0].Session.ID, out); closed {
		t.Error("closed twice")
	}
	sessions, _ := s.SessionsOverlapping(ctx, u1, in, now)
	if len(sessions) != 1 || !sessions[0].AutoClosed || len(sessions[0].Breaks) != 1 {
		t.Fatalf("after close: %+v", sessions)
	}
	if net := attendance.NetByDay(sessions, now, ist)[attendance.DayKey{Day: "2026-11-24", TeamID: teamA}]; net != 8*time.Hour {
		t.Errorf("net = %v, want 8h", net)
	}
}

func TestIntegrationWFHConcurrentDecisions(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	id, err := s.CreateWFH(ctx, attendance.WFHRequest{UserID: u1, TeamID: teamA, Start: "2026-11-26", End: "2026-11-26", Reason: "plumber"})
	if err != nil {
		t.Fatal(err)
	}
	const clicks = 10
	var wg sync.WaitGroup
	results := make(chan error, clicks)
	for i := 0; i < clicks; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status := attendance.WFHApproved
			if i%2 == 1 {
				status = attendance.WFHRejected
			}
			_, err := s.DecideWFH(ctx, id, teamA, status, admin, "x", time.Now())
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	ok, already := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, attendance.ErrAlreadyDecided):
			already++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 1 || already != clicks-1 {
		t.Fatalf("decisions: %d ok, %d already decided", ok, already)
	}
	// Wrong team can't decide.
	id2, _ := s.CreateWFH(ctx, attendance.WFHRequest{UserID: u2, TeamID: teamA, Start: "2026-11-27", End: "2026-11-27", Reason: "x"})
	if _, err := s.DecideWFH(ctx, id2, teamB, attendance.WFHApproved, admin, "", time.Now()); !errors.Is(err, attendance.ErrAlreadyDecided) {
		t.Errorf("decided from another team: %v", err)
	}
}

func TestIntegrationWFHRules(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	mk := func(user, team, start, end string, half bool) (int64, error) {
		return s.CreateWFH(ctx, attendance.WFHRequest{UserID: user, TeamID: team, Start: start, End: end, HalfDay: half, Reason: "r"})
	}
	full, err := mk(u1, teamA, "2026-11-26", "2026-11-27", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mk(u1, teamA, "2026-11-27", "2026-11-28", false); !errors.Is(err, attendance.ErrWFHOverlap) {
		t.Errorf("overlap in same team: %v", err)
	}
	if _, err := mk(u1, teamB, "2026-11-27", "2026-11-28", false); err != nil {
		t.Errorf("other team should be independent: %v", err)
	}
	half, _ := mk(u1, teamA, "2026-11-30", "2026-11-30", true)
	for _, id := range []int64{full, half} {
		if _, err := s.DecideWFH(ctx, id, teamA, attendance.WFHApproved, admin, "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := s.WFHApprovedOn(ctx, u1, teamA, "2026-11-27"); !ok {
		t.Error("approved full day not found")
	}
	if ok, _ := s.WFHApprovedOn(ctx, u1, teamA, "2026-11-30"); ok {
		t.Error("half day must not switch the mode")
	}
	if ok, _ := s.WFHApprovedOn(ctx, u1, teamB, "2026-11-27"); ok {
		t.Error("pending request in team B counted")
	}
	if n, _ := s.ApprovedWFHDays(ctx, u1, teamA, "2026-11-01", "2026-11-30"); n != 2.5 {
		t.Errorf("approved days = %v, want 2.5", n)
	}

	// Cancel: approved but started → no; approved future → yes; someone else's → no.
	if r, _ := s.CancelWFH(ctx, full, u1, "2026-11-26"); r != nil {
		t.Error("cancelled an approved request that already started")
	}
	if r, _ := s.CancelWFH(ctx, half, u2, "2026-11-25"); r != nil {
		t.Error("cancelled someone else's request")
	}
	if r, err := s.CancelWFH(ctx, half, u1, "2026-11-25"); err != nil || r == nil || r.Status != attendance.WFHCancelled {
		t.Errorf("cancel future approved: %v %+v", err, r)
	}

	// Same-day approval switches the open session.
	if _, err := s.InsertSession(ctx, u1, teamA, at(2026, 11, 26, 9, 0), "office", ""); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.SetOpenSessionMode(ctx, u1, teamA, "wfh"); err != nil || !changed {
		t.Errorf("set mode: %v %v", changed, err)
	}
	if changed, _ := s.SetOpenSessionMode(ctx, u1, teamA, "wfh"); changed {
		t.Error("second switch reported a change")
	}
}

func TestIntegrationJobRunsAndHolidays(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	if ok, _ := s.ClaimJobRun(ctx, "daily_summary", "2026-11-25/inkyank"); !ok {
		t.Fatal("first claim failed")
	}
	if ok, _ := s.ClaimJobRun(ctx, "daily_summary", "2026-11-25/inkyank"); ok {
		t.Error("job ran twice")
	}
	if ok, _ := s.ClaimJobRun(ctx, "daily_summary", "2026-11-25/seechanlabs"); !ok {
		t.Error("other team blocked")
	}

	if _, err := s.AddHoliday(ctx, "2026-12-25", "", "Christmas"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddHoliday(ctx, "2026-12-25", "", "dup"); !errors.Is(err, attendance.ErrHolidayExists) {
		t.Errorf("duplicate global holiday: %v", err)
	}
	if _, err := s.AddHoliday(ctx, "2026-12-26", teamA, "Team day"); err != nil {
		t.Fatal(err)
	}
	a, _ := s.ListHolidays(ctx, teamA, "2026-12-01")
	b, _ := s.ListHolidays(ctx, teamB, "2026-12-01")
	if len(a) != 2 || len(b) != 1 {
		t.Errorf("team A sees %d, team B sees %d (want 2, 1)", len(a), len(b))
	}
}

func TestIntegrationMemberSettingsSurviveSync(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	off, hours := false, 6.5
	before, after, err := s.UpdateMember(ctx, u1, attendance.MemberUpdate{Tracked: &off, ExpectedHours: &hours})
	if err != nil || !before.Tracked || after.Tracked || after.ExpectedHours != 6.5 {
		t.Fatalf("update: %v %+v %+v", err, before, after)
	}
	if err := s.SyncMembers(ctx, []mattermost.User{{ID: u1, Username: "priya.k"}, {ID: "newnewnewnewnewnewnewnew01", Username: "new"}}); err != nil {
		t.Fatal(err)
	}
	all, err := s.AllMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m := all[u1]; m.Tracked || m.ExpectedHours != 6.5 || m.Username != "priya.k" || m.Since.IsZero() {
		t.Errorf("sync overwrote settings: %+v", m)
	}
	if m := all["newnewnewnewnewnewnewnew01"]; !m.Tracked || m.ExpectedHours != 8 || !m.RemindersOn {
		t.Errorf("new member defaults: %+v", m)
	}
}

func TestIntegrationGitHubLogin(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	login := "Jana-Dev"
	_, after, err := s.UpdateMember(ctx, u1, attendance.MemberUpdate{GitHubLogin: &login})
	if err != nil || after.GitHubLogin != "Jana-Dev" {
		t.Fatalf("link: %v %+v", err, after)
	}
	if m, _ := s.GetMember(ctx, u1); m.GitHubLogin != "Jana-Dev" || !m.Tracked {
		t.Errorf("stored: %+v", m)
	}
	// Logins are case-insensitive: another member can't take "jana-dev".
	other := "jana-dev"
	if _, _, err := s.UpdateMember(ctx, u2, attendance.MemberUpdate{GitHubLogin: &other}); !errors.Is(err, attendance.ErrGitHubLoginTaken) {
		t.Fatalf("duplicate: want ErrGitHubLoginTaken, got %v", err)
	}
	// Relinking the same member and unlinking both work; unlinked is NULL,
	// so several unlinked members don't collide.
	if _, _, err := s.UpdateMember(ctx, u1, attendance.MemberUpdate{GitHubLogin: &other}); err != nil {
		t.Fatalf("relink same member: %v", err)
	}
	none := ""
	for _, u := range []string{u1, u2} {
		if _, after, err := s.UpdateMember(ctx, u, attendance.MemberUpdate{GitHubLogin: &none}); err != nil || after.GitHubLogin != "" {
			t.Fatalf("unlink %s: %v", u, err)
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM members WHERE github_login IS NULL`); n < 2 {
		t.Errorf("unlinked logins should be NULL, got %d NULL rows", n)
	}
}

func TestIntegrationEODResubmitUpdates(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	r := attendance.EODReport{UserID: u1, TeamID: teamA, Day: "2026-11-26", DidToday: "First version of the day",
		Items: []attendance.EODItem{
			{IssueURL: "https://github.com/o/r/issues/1", Title: "One", OldStatus: "Todo", NewStatus: "In progress"},
			{IssueURL: "https://github.com/o/r/issues/2", Title: "Two", OldStatus: "In progress", NewStatus: "In progress", Synced: true},
		}}
	first, err := s.SaveEODReport(ctx, r)
	if err != nil || first.ID == 0 || first.SyncStatus != "pending" {
		t.Fatalf("first save: %v %+v", err, first)
	}
	if _, created, err := s.EnsureEODReply(ctx, first.ID, func() (string, error) { return "reply-1", nil }); err != nil || !created {
		t.Fatalf("reply: %v %v", err, created)
	}

	r.DidToday, r.Blockers = "Second version", "Waiting for review"
	r.Items = []attendance.EODItem{{IssueURL: "https://github.com/o/r/issues/2", Title: "Two", OldStatus: "In progress", NewStatus: "In progress", Synced: true}}
	second, err := s.SaveEODReport(ctx, r)
	if err != nil || second.ID != first.ID || second.ReplyPostID != "reply-1" || second.SyncStatus != "synced" {
		t.Fatalf("resubmit should update the same report: %v %+v", err, second)
	}
	got, err := s.GetEODReport(ctx, u1, teamA, "2026-11-26")
	if err != nil || got.DidToday != "Second version" || got.Blockers != "Waiting for review" || len(got.Items) != 1 || got.ReplyPostID != "reply-1" {
		t.Fatalf("stored: %v %+v", err, got)
	}
	if n := countRows(t, s, `SELECT count(*) FROM eod_reports`); n != 1 {
		t.Errorf("%d reports, want 1", n)
	}
	// The reply is created once; later calls return it.
	calls := 0
	if id, created, _ := s.EnsureEODReply(ctx, first.ID, func() (string, error) { calls++; return "reply-2", nil }); id != "reply-1" || created || calls != 0 {
		t.Errorf("second EnsureEODReply: %q %v calls=%d", id, created, calls)
	}
	if none, _ := s.GetEODReport(ctx, u2, teamA, "2026-11-26"); none != nil {
		t.Error("other member has no report")
	}
}

func TestIntegrationEODThreadConcurrent(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	var (
		mu    sync.Mutex
		calls int
		wg    sync.WaitGroup
	)
	ids := make([]string, 10)
	errs := make([]error, 10)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], errs[i] = s.EnsureEODThread(ctx, teamA, "2026-11-26", func() (string, error) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				time.Sleep(20 * time.Millisecond) // widen the race window
				return fmt.Sprintf("root-%d", calls), nil
			})
		}(i)
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil || ids[i] != "root-1" {
			t.Fatalf("caller %d: %q %v", i, ids[i], errs[i])
		}
	}
	if calls != 1 {
		t.Errorf("root post created %d times, want 1", calls)
	}
	// Another team or day gets its own root; a failed create leaves none.
	if id, _ := s.EnsureEODThread(ctx, teamB, "2026-11-26", func() (string, error) { return "root-b", nil }); id != "root-b" {
		t.Errorf("team B: %q", id)
	}
	if _, err := s.EnsureEODThread(ctx, teamA, "2026-11-27", func() (string, error) { return "", fmt.Errorf("mattermost down") }); err == nil {
		t.Error("create error should be returned")
	}
	if n := countRows(t, s, `SELECT count(*) FROM eod_threads`); n != 2 {
		t.Errorf("%d threads, want 2", n)
	}
}
