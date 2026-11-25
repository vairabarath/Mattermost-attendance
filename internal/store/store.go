// Package store implements persistence with pgx. All SQL is parameterized.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

// Store wraps a pgx connection pool.
type Store struct {
	Pool *pgxpool.Pool
}

// New connects to Postgres.
func New(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	return &Store{Pool: pool}, nil
}

// Ping checks database connectivity (used by /healthz).
func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

// Close releases the pool.
func (s *Store) Close() { s.Pool.Close() }

func (s *Store) MemberRefreshedAt(ctx context.Context, userID string) (time.Time, bool, error) {
	var t time.Time
	err := s.Pool.QueryRow(ctx, `SELECT refreshed_at FROM members WHERE mm_user_id = $1`, userID).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return t, err == nil, err
}

func (s *Store) UpsertMember(ctx context.Context, userID, username, displayName string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO members (mm_user_id, username, display_name, refreshed_at)
		VALUES ($1, $2, NULLIF($3, ''), now())
		ON CONFLICT (mm_user_id) DO UPDATE
		SET username = EXCLUDED.username,
		    display_name = EXCLUDED.display_name,
		    refreshed_at = now()`,
		userID, username, displayName)
	return err
}

func (s *Store) InsertSession(ctx context.Context, userID, teamID string, checkIn time.Time, mode, note string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO sessions (mm_user_id, team_id, check_in, work_mode, in_note)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		RETURNING id`,
		userID, teamID, checkIn.UTC(), mode, note).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "one_open_session" {
		return 0, attendance.ErrAlreadyCheckedIn
	}
	return id, err
}

func (s *Store) OpenSession(ctx context.Context, userID string) (*attendance.Session, error) {
	var sess attendance.Session
	err := s.Pool.QueryRow(ctx, `
		SELECT id, team_id, check_in, work_mode
		FROM sessions WHERE mm_user_id = $1 AND check_out IS NULL`, userID).
		Scan(&sess.ID, &sess.TeamID, &sess.CheckIn, &sess.WorkMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	breaks, err := loadBreaks(ctx, s.Pool, []int64{sess.ID})
	if err != nil {
		return nil, err
	}
	sess.Breaks = breaks[sess.ID]
	return &sess, nil
}

// CloseOpenSession closes the open session and any open break in one
// transaction, then returns the closed session with its breaks.
func (s *Store) CloseOpenSession(ctx context.Context, userID string, out time.Time, summary string) (*attendance.Session, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	sess, err := openSessionTx(ctx, tx, userID)
	if err != nil {
		return nil, err
	}

	// The table requires check_out > check_in; guard against clock skew.
	if !out.After(sess.CheckIn) {
		out = sess.CheckIn.Add(time.Second)
	}
	out = out.UTC()

	if _, err = tx.Exec(ctx, `UPDATE breaks SET end_at = $2 WHERE session_id = $1 AND end_at IS NULL`, sess.ID, out); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET check_out = $2, out_summary = NULLIF($3, '') WHERE id = $1`,
		sess.ID, out, summary); err != nil {
		return nil, err
	}
	breaks, err := loadBreaks(ctx, tx, []int64{sess.ID})
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	sess.CheckOut = &out
	sess.Breaks = breaks[sess.ID]
	return sess, nil
}

func (s *Store) SessionsOverlapping(ctx context.Context, userID string, from, to time.Time) ([]attendance.Session, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, team_id, check_in, check_out, work_mode
		FROM sessions
		WHERE mm_user_id = $1 AND check_in < $3 AND (check_out IS NULL OR check_out > $2)
		ORDER BY check_in`, userID, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	sessions, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (attendance.Session, error) {
		var s attendance.Session
		err := r.Scan(&s.ID, &s.TeamID, &s.CheckIn, &s.CheckOut, &s.WorkMode)
		return s, err
	})
	if err != nil || len(sessions) == 0 {
		return sessions, err
	}

	ids := make([]int64, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	breaks, err := loadBreaks(ctx, s.Pool, ids)
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		sessions[i].Breaks = breaks[sessions[i].ID]
	}
	return sessions, nil
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func loadBreaks(ctx context.Context, q querier, sessionIDs []int64) (map[int64][]attendance.Break, error) {
	rows, err := q.Query(ctx, `
		SELECT session_id, start_at, end_at FROM breaks
		WHERE session_id = ANY($1) ORDER BY start_at`, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]attendance.Break)
	for rows.Next() {
		var id int64
		var b attendance.Break
		if err := rows.Scan(&id, &b.Start, &b.End); err != nil {
			return nil, err
		}
		out[id] = append(out[id], b)
	}
	return out, rows.Err()
}

// openSessionTx locks and returns the user's open session inside tx.
func openSessionTx(ctx context.Context, tx pgx.Tx, userID string) (*attendance.Session, error) {
	var sess attendance.Session
	err := tx.QueryRow(ctx, `
		SELECT id, team_id, check_in, work_mode
		FROM sessions WHERE mm_user_id = $1 AND check_out IS NULL
		FOR UPDATE`, userID).Scan(&sess.ID, &sess.TeamID, &sess.CheckIn, &sess.WorkMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, attendance.ErrNotCheckedIn
	}
	return &sess, err
}

// StartBreak opens a break on the user's open session.
func (s *Store) StartBreak(ctx context.Context, userID string, at time.Time) (*attendance.Session, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	sess, err := openSessionTx(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	if at.Before(sess.CheckIn) {
		at = sess.CheckIn
	}
	_, err = tx.Exec(ctx, `INSERT INTO breaks (session_id, start_at) VALUES ($1, $2)`, sess.ID, at.UTC())
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "one_open_break" {
		return nil, attendance.ErrAlreadyOnBreak
	}
	if err != nil {
		return nil, err
	}
	return sess, tx.Commit(ctx)
}

// EndBreak closes the open break on the user's open session.
func (s *Store) EndBreak(ctx context.Context, userID string, at time.Time) (*attendance.Session, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	sess, err := openSessionTx(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE breaks SET end_at = GREATEST($2, start_at)
		WHERE session_id = $1 AND end_at IS NULL`, sess.ID, at.UTC())
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, attendance.ErrNotOnBreak
	}
	return sess, tx.Commit(ctx)
}

// TeamOpenSessions lists everyone currently checked in to a team.
func (s *Store) TeamOpenSessions(ctx context.Context, teamID string) ([]attendance.WorkingMember, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT s.mm_user_id, m.username, COALESCE(m.display_name, ''), s.check_in, s.work_mode,
		       (SELECT b.start_at FROM breaks b WHERE b.session_id = s.id AND b.end_at IS NULL)
		FROM sessions s JOIN members m USING (mm_user_id)
		WHERE s.team_id = $1 AND s.check_out IS NULL
		ORDER BY s.check_in`, teamID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (attendance.WorkingMember, error) {
		var w attendance.WorkingMember
		err := r.Scan(&w.UserID, &w.Username, &w.DisplayName, &w.CheckIn, &w.WorkMode, &w.BreakSince)
		return w, err
	})
}

// SaveCustomStatus stores the member's own custom status (raw JSON).
func (s *Store) SaveCustomStatus(ctx context.Context, userID string, raw []byte) error {
	_, err := s.Pool.Exec(ctx, `UPDATE members SET saved_custom_status = $2 WHERE mm_user_id = $1`, userID, raw)
	return err
}

// TakeSavedCustomStatus returns the saved status (nil if none) and clears it.
func (s *Store) TakeSavedCustomStatus(ctx context.Context, userID string) ([]byte, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `
		WITH old AS (
			SELECT saved_custom_status AS v FROM members WHERE mm_user_id = $1 FOR UPDATE
		)
		UPDATE members SET saved_custom_status = NULL
		WHERE mm_user_id = $1
		RETURNING (SELECT v FROM old)`, userID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}
