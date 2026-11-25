package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// ClaimJobRun inserts job+key; false means it already ran.
func (s *Store) ClaimJobRun(ctx context.Context, job, key string) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `INSERT INTO job_runs (job, run_key) VALUES ($1, $2) ON CONFLICT DO NOTHING`, job, key)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SyncMembers registers/refreshes members seen in team rosters. Settings
// (tracked, hours, reminders) are never touched here.
func (s *Store) SyncMembers(ctx context.Context, users []mattermost.User) error {
	if len(users) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, u := range users {
		batch.Queue(`
			INSERT INTO members (mm_user_id, username, display_name, active, refreshed_at)
			VALUES ($1, $2, NULLIF($3, ''), $4, now())
			ON CONFLICT (mm_user_id) DO UPDATE
			SET username = EXCLUDED.username, display_name = EXCLUDED.display_name,
			    active = EXCLUDED.active, refreshed_at = now()`,
			u.ID, u.Username, u.DisplayName(), u.DeleteAt == 0)
	}
	return s.Pool.SendBatch(ctx, batch).Close()
}

func (s *Store) AllMembers(ctx context.Context) (map[string]attendance.MemberSettings, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+memberCols+` FROM members`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (attendance.MemberSettings, error) { return scanMember(r) })
	if err != nil {
		return nil, err
	}
	out := make(map[string]attendance.MemberSettings, len(list))
	for _, m := range list {
		out[m.UserID] = m
	}
	return out, nil
}

func (s *Store) AllSessionsOverlapping(ctx context.Context, from, to time.Time) (map[string][]attendance.Session, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT mm_user_id, id, team_id, check_in, check_out, work_mode, auto_closed
		FROM sessions
		WHERE check_in < $2 AND (check_out IS NULL OR check_out > $1)
		ORDER BY check_in`, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	type row struct {
		user string
		s    attendance.Session
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.user, &x.s.ID, &x.s.TeamID, &x.s.CheckIn, &x.s.CheckOut, &x.s.WorkMode, &x.s.AutoClosed)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(list))
	for i, x := range list {
		ids[i] = x.s.ID
	}
	breaks, err := loadBreaks(ctx, s.Pool, ids)
	if err != nil {
		return nil, err
	}
	out := map[string][]attendance.Session{}
	for _, x := range list {
		x.s.Breaks = breaks[x.s.ID]
		out[x.user] = append(out[x.user], x.s)
	}
	return out, nil
}

func (s *Store) OpenSessions(ctx context.Context, openedBefore time.Time) ([]attendance.OpenSessionInfo, error) {
	if openedBefore.IsZero() {
		openedBefore = time.Now().Add(time.Hour)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT s.mm_user_id, m.username, m.expected_hours::float8, s.id, s.team_id, s.check_in, s.work_mode
		FROM sessions s JOIN members m USING (mm_user_id)
		WHERE s.check_out IS NULL AND s.check_in < $1
		ORDER BY s.check_in`, openedBefore.UTC())
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (attendance.OpenSessionInfo, error) {
		var o attendance.OpenSessionInfo
		err := r.Scan(&o.UserID, &o.Username, &o.Expected, &o.Session.ID, &o.Session.TeamID, &o.Session.CheckIn, &o.Session.WorkMode)
		return o, err
	})
	if err != nil || len(list) == 0 {
		return list, err
	}
	ids := make([]int64, len(list))
	for i, o := range list {
		ids[i] = o.Session.ID
	}
	breaks, err := loadBreaks(ctx, s.Pool, ids)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Session.Breaks = breaks[list[i].Session.ID]
	}
	return list, nil
}

// AutoCloseSession closes a still-open session at out: breaks starting at or
// after out are dropped, breaks running past out end at out.
func (s *Store) AutoCloseSession(ctx context.Context, sessionID int64, out time.Time) (time.Time, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var checkIn time.Time
	err = tx.QueryRow(ctx, `SELECT check_in FROM sessions WHERE id = $1 AND check_out IS NULL FOR UPDATE`, sessionID).Scan(&checkIn)
	if err == pgx.ErrNoRows {
		return time.Time{}, false, nil // checked out meanwhile
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if !out.After(checkIn) {
		out = checkIn.Add(time.Second)
	}
	out = out.UTC()
	if _, err = tx.Exec(ctx, `DELETE FROM breaks WHERE session_id = $1 AND start_at >= $2`, sessionID, out); err != nil {
		return time.Time{}, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE breaks SET end_at = $2 WHERE session_id = $1 AND (end_at IS NULL OR end_at > $2)`, sessionID, out); err != nil {
		return time.Time{}, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET check_out = $2, auto_closed = true WHERE id = $1`, sessionID, out); err != nil {
		return time.Time{}, false, err
	}
	return out, true, tx.Commit(ctx)
}

var (
	_ attendance.Store    = (*Store)(nil)
	_ attendance.JobStore = (*Store)(nil)
)
