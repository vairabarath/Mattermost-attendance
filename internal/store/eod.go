package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

var _ attendance.EODStore = (*Store)(nil)

// SaveEODReport upserts the report for (user, team, day) and replaces its
// items, in one transaction. A resubmission keeps the id, submitted_at and
// reply_post_id and goes back to sync_status 'pending' if anything changed.
func (s *Store) SaveEODReport(ctx context.Context, r attendance.EODReport) (attendance.EODReport, error) {
	day, err := dateParam(r.Day)
	if err != nil {
		return r, err
	}
	sync := "synced"
	for _, it := range r.Items {
		if !it.Synced {
			sync = "pending"
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return r, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var reply *string
	err = tx.QueryRow(ctx, `
		INSERT INTO eod_reports (mm_user_id, team_id, day, did_today, blockers, tomorrow, sync_status)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7)
		ON CONFLICT (mm_user_id, team_id, day) DO UPDATE SET
			did_today = EXCLUDED.did_today, blockers = EXCLUDED.blockers, tomorrow = EXCLUDED.tomorrow,
			sync_status = EXCLUDED.sync_status, sync_error = NULL, updated_at = now()
		RETURNING id, reply_post_id, submitted_at, updated_at, sync_status`,
		r.UserID, r.TeamID, day, r.DidToday, r.Blockers, r.Tomorrow, sync,
	).Scan(&r.ID, &reply, &r.SubmittedAt, &r.UpdatedAt, &r.SyncStatus)
	if err != nil {
		return r, err
	}
	if reply != nil {
		r.ReplyPostID = *reply
	}
	if _, err := tx.Exec(ctx, `DELETE FROM eod_items WHERE report_id = $1`, r.ID); err != nil {
		return r, err
	}
	for _, it := range r.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO eod_items (report_id, issue_url, issue_title, old_status, new_status, synced)
			VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6)
			ON CONFLICT (report_id, issue_url) DO NOTHING`,
			r.ID, it.IssueURL, it.Title, it.OldStatus, it.NewStatus, it.Synced); err != nil {
			return r, err
		}
	}
	return r, tx.Commit(ctx)
}

func (s *Store) GetEODReport(ctx context.Context, userID, teamID, day string) (*attendance.EODReport, error) {
	d, err := dateParam(day)
	if err != nil {
		return nil, err
	}
	r := attendance.EODReport{UserID: userID, TeamID: teamID, Day: day}
	err = s.Pool.QueryRow(ctx, `
		SELECT id, did_today, COALESCE(blockers, ''), COALESCE(tomorrow, ''), sync_status,
		       COALESCE(reply_post_id, ''), submitted_at, updated_at
		FROM eod_reports WHERE mm_user_id = $1 AND team_id = $2 AND day = $3`, userID, teamID, d,
	).Scan(&r.ID, &r.DidToday, &r.Blockers, &r.Tomorrow, &r.SyncStatus, &r.ReplyPostID, &r.SubmittedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT issue_url, COALESCE(issue_title, ''), COALESCE(old_status, ''), new_status, synced
		FROM eod_items WHERE report_id = $1 ORDER BY issue_url`, r.ID)
	if err != nil {
		return nil, err
	}
	r.Items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (attendance.EODItem, error) {
		var it attendance.EODItem
		err := row.Scan(&it.IssueURL, &it.Title, &it.OldStatus, &it.NewStatus, &it.Synced)
		return it, err
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// EnsureEODThread serializes callers on (team, day) with a transaction-level
// advisory lock, so concurrent first submissions create exactly one root.
func (s *Store) EnsureEODThread(ctx context.Context, teamID, day string, create func() (string, error)) (string, error) {
	d, err := dateParam(day)
	if err != nil {
		return "", err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('eod_thread:' || $1 || ':' || $2, 0))`, teamID, day); err != nil {
		return "", err
	}
	var root string
	err = tx.QueryRow(ctx, `SELECT root_post_id FROM eod_threads WHERE team_id = $1 AND day = $2`, teamID, d).Scan(&root)
	if err == nil {
		return root, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if root, err = create(); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO eod_threads (team_id, day, root_post_id) VALUES ($1, $2, $3)`, teamID, d, root); err != nil {
		return "", err
	}
	return root, tx.Commit(ctx)
}

// EnsureEODReply locks the report row, so two quick submissions of the same
// report can't both create a reply.
func (s *Store) EnsureEODReply(ctx context.Context, reportID int64, create func() (string, error)) (string, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var reply *string
	if err := tx.QueryRow(ctx, `SELECT reply_post_id FROM eod_reports WHERE id = $1 FOR UPDATE`, reportID).Scan(&reply); err != nil {
		return "", false, err
	}
	if reply != nil && *reply != "" {
		return *reply, false, tx.Commit(ctx)
	}
	id, err := create()
	if err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE eod_reports SET reply_post_id = $2 WHERE id = $1`, reportID, id); err != nil {
		return "", false, err
	}
	return id, true, tx.Commit(ctx)
}
