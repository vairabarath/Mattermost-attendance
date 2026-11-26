package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

var (
	_ attendance.EODStore     = (*Store)(nil)
	_ attendance.EODSyncStore = (*Store)(nil)
)

// SaveEODReport upserts the report for (user, team, day) and replaces its
// items, in one transaction. A resubmission keeps the id, submitted_at and
// reply_post_id and goes back to sync_status 'pending' if anything changed.
func (s *Store) SaveEODReport(ctx context.Context, r attendance.EODReport) (attendance.EODReport, error) {
	day, err := dateParam(r.Day)
	if err != nil {
		return r, err
	}
	sync := r.SyncStatus
	if sync == "" {
		sync = "synced"
		for _, it := range r.Items {
			if !it.Synced {
				sync = "pending"
			}
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
			sync_status = EXCLUDED.sync_status, sync_error = NULL, sync_attempts = 0,
			sync_claimed_until = NULL, updated_at = now()
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

const eodReportCols = `id, mm_user_id, team_id, to_char(day, 'YYYY-MM-DD'), did_today, COALESCE(blockers, ''),
	COALESCE(tomorrow, ''), sync_status, COALESCE(sync_error, ''), sync_attempts, gh_activity,
	COALESCE(reply_post_id, ''), COALESCE(blocker_post_id, ''), submitted_at, updated_at`

func scanEODReport(row pgx.Row) (*attendance.EODReport, error) {
	var (
		r   attendance.EODReport
		act []byte
	)
	err := row.Scan(&r.ID, &r.UserID, &r.TeamID, &r.Day, &r.DidToday, &r.Blockers, &r.Tomorrow, &r.SyncStatus,
		&r.SyncError, &r.SyncAttempts, &act, &r.ReplyPostID, &r.BlockerPostID, &r.SubmittedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(act) > 0 {
		r.Activity = &attendance.Activity{}
		if err := json.Unmarshal(act, r.Activity); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

func (s *Store) loadEODItems(ctx context.Context, r *attendance.EODReport) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT issue_url, COALESCE(issue_title, ''), COALESCE(old_status, ''), new_status, synced
		FROM eod_items WHERE report_id = $1 ORDER BY issue_url`, r.ID)
	if err != nil {
		return err
	}
	r.Items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (attendance.EODItem, error) {
		var it attendance.EODItem
		err := row.Scan(&it.IssueURL, &it.Title, &it.OldStatus, &it.NewStatus, &it.Synced)
		return it, err
	})
	return err
}

func (s *Store) GetEODReport(ctx context.Context, userID, teamID, day string) (*attendance.EODReport, error) {
	d, err := dateParam(day)
	if err != nil {
		return nil, err
	}
	r, err := scanEODReport(s.Pool.QueryRow(ctx, `SELECT `+eodReportCols+` FROM eod_reports
		WHERE mm_user_id = $1 AND team_id = $2 AND day = $3`, userID, teamID, d))
	if err != nil || r == nil {
		return nil, err
	}
	return r, s.loadEODItems(ctx, r)
}

func (s *Store) GetEODReportByID(ctx context.Context, id int64) (*attendance.EODReport, error) {
	r, err := scanEODReport(s.Pool.QueryRow(ctx, `SELECT `+eodReportCols+` FROM eod_reports WHERE id = $1`, id))
	if err != nil || r == nil {
		return nil, err
	}
	return r, s.loadEODItems(ctx, r)
}

// ClaimEODSync takes a lease on an unsynced report and counts an attempt.
func (s *Store) ClaimEODSync(ctx context.Context, id int64, lease time.Duration) (*attendance.EODReport, error) {
	r, err := scanEODReport(s.Pool.QueryRow(ctx, `
		UPDATE eod_reports SET sync_claimed_until = now() + $2 * interval '1 second', sync_attempts = sync_attempts + 1
		WHERE id = $1 AND sync_status <> 'synced' AND (sync_claimed_until IS NULL OR sync_claimed_until < now())
		RETURNING `+eodReportCols, id, int64(lease/time.Second)))
	if err != nil || r == nil {
		return nil, err
	}
	return r, s.loadEODItems(ctx, r)
}

func (s *Store) MarkEODItemSynced(ctx context.Context, reportID int64, issueURL string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE eod_items SET synced = true WHERE report_id = $1 AND issue_url = $2`, reportID, issueURL)
	return err
}

func (s *Store) FinishEODSync(ctx context.Context, id int64, status, syncErr string, act *attendance.Activity) error {
	var actJSON []byte
	if act != nil {
		var err error
		if actJSON, err = json.Marshal(act); err != nil {
			return err
		}
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE eod_reports SET sync_status = $2, sync_error = NULLIF($3, ''),
			gh_activity = COALESCE($4::jsonb, gh_activity), sync_claimed_until = NULL
		WHERE id = $1`, id, status, syncErr, actJSON)
	return err
}

func (s *Store) PendingEODSyncs(ctx context.Context, maxAttempts int, since time.Time) ([]int64, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id FROM eod_reports
		WHERE sync_status <> 'synced' AND sync_attempts < $1 AND updated_at >= $2
		  AND (sync_claimed_until IS NULL OR sync_claimed_until < now())
		ORDER BY updated_at`, maxAttempts, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
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
	return s.ensureReportPost(ctx, reportID, "reply_post_id", create)
}

// EnsureEODBlockerPost is the same for the report's admin alert.
func (s *Store) EnsureEODBlockerPost(ctx context.Context, reportID int64, create func() (string, error)) (string, bool, error) {
	return s.ensureReportPost(ctx, reportID, "blocker_post_id", create)
}

// ensureReportPost returns the post id in column (a constant, never user
// input), calling create once while holding the report's row lock.
func (s *Store) ensureReportPost(ctx context.Context, reportID int64, column string, create func() (string, error)) (string, bool, error) {
	if column != "reply_post_id" && column != "blocker_post_id" {
		return "", false, errors.New("bad column")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var post *string
	if err := tx.QueryRow(ctx, `SELECT `+column+` FROM eod_reports WHERE id = $1 FOR UPDATE`, reportID).Scan(&post); err != nil {
		return "", false, err
	}
	if post != nil && *post != "" {
		return *post, false, tx.Commit(ctx)
	}
	id, err := create()
	if err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE eod_reports SET `+column+` = $2 WHERE id = $1`, reportID, id); err != nil {
		return "", false, err
	}
	return id, true, tx.Commit(ctx)
}
