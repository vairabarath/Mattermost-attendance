package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

const wfhCols = `w.id, w.mm_user_id, m.username, w.team_id,
	to_char(w.start_date, 'YYYY-MM-DD'), to_char(w.end_date, 'YYYY-MM-DD'),
	w.half_day, w.reason, w.status::text, COALESCE(w.decided_by, ''), COALESCE(d.username, ''),
	w.decided_at, COALESCE(w.decision_note, ''), COALESCE(w.admin_post_id, ''), w.created_at`

const wfhFrom = ` FROM wfh_requests w JOIN members m ON m.mm_user_id = w.mm_user_id
	LEFT JOIN members d ON d.mm_user_id = w.decided_by `

func scanWFH(r pgx.Row) (attendance.WFHRequest, error) {
	var x attendance.WFHRequest
	err := r.Scan(&x.ID, &x.UserID, &x.Username, &x.TeamID, &x.Start, &x.End, &x.HalfDay, &x.Reason,
		&x.Status, &x.DecidedBy, &x.DecidedByName, &x.DecidedAt, &x.Note, &x.AdminPostID, &x.CreatedAt)
	return x, err
}

func collectWFH(rows pgx.Rows, err error) ([]attendance.WFHRequest, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (attendance.WFHRequest, error) { return scanWFH(r) })
}

// CreateWFH inserts a pending request unless it overlaps the member's
// pending/approved requests in the team (checked under a member row lock).
func (s *Store) CreateWFH(ctx context.Context, r attendance.WFHRequest) (int64, error) {
	start, err := dateParam(r.Start)
	if err != nil {
		return 0, err
	}
	end, err := dateParam(r.End)
	if err != nil {
		return 0, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err = tx.Exec(ctx, `SELECT 1 FROM members WHERE mm_user_id = $1 FOR UPDATE`, r.UserID); err != nil {
		return 0, err
	}
	var overlap bool
	if err = tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM wfh_requests WHERE mm_user_id = $1 AND team_id = $2
		               AND status IN ('pending','approved') AND start_date <= $4 AND end_date >= $3)`,
		r.UserID, r.TeamID, start, end).Scan(&overlap); err != nil {
		return 0, err
	}
	if overlap {
		return 0, attendance.ErrWFHOverlap
	}
	var id int64
	if err = tx.QueryRow(ctx, `
		INSERT INTO wfh_requests (mm_user_id, team_id, start_date, end_date, half_day, reason)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		r.UserID, r.TeamID, start, end, r.HalfDay, r.Reason).Scan(&id); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) GetWFH(ctx context.Context, id int64) (*attendance.WFHRequest, error) {
	x, err := scanWFH(s.Pool.QueryRow(ctx, `SELECT `+wfhCols+wfhFrom+`WHERE w.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &x, nil
}

func (s *Store) SetWFHAdminPost(ctx context.Context, id int64, postID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE wfh_requests SET admin_post_id = $2 WHERE id = $1`, id, postID)
	return err
}

func (s *Store) ListWFH(ctx context.Context, userID, teamID string, limit int) ([]attendance.WFHRequest, error) {
	return collectWFH(s.Pool.Query(ctx, `SELECT `+wfhCols+wfhFrom+`
		WHERE w.mm_user_id = $1 AND w.team_id = $2 ORDER BY w.start_date DESC, w.id DESC LIMIT $3`,
		userID, teamID, limit))
}

func (s *Store) ListTeamWFH(ctx context.Context, teamID, fromDay string, limit int) ([]attendance.WFHRequest, error) {
	from, err := dateParam(fromDay)
	if err != nil {
		return nil, err
	}
	return collectWFH(s.Pool.Query(ctx, `SELECT `+wfhCols+wfhFrom+`
		WHERE w.team_id = $1 AND w.end_date >= $2
		ORDER BY w.start_date, w.id LIMIT $3`, teamID, from, limit))
}

func (s *Store) PendingWFH(ctx context.Context, teamID, coveringDay string) ([]attendance.WFHRequest, error) {
	if coveringDay == "" {
		return collectWFH(s.Pool.Query(ctx, `SELECT `+wfhCols+wfhFrom+`
			WHERE w.team_id = $1 AND w.status = 'pending' ORDER BY w.start_date, w.id`, teamID))
	}
	day, err := dateParam(coveringDay)
	if err != nil {
		return nil, err
	}
	return collectWFH(s.Pool.Query(ctx, `SELECT `+wfhCols+wfhFrom+`
		WHERE w.team_id = $1 AND w.status = 'pending' AND w.start_date <= $2 AND w.end_date >= $2
		ORDER BY w.start_date, w.id`, teamID, day))
}

// DecideWFH: the WHERE status = 'pending' guard means two admins clicking
// at once produce exactly one decision.
func (s *Store) DecideWFH(ctx context.Context, id int64, teamID, status, deciderID, note string, at time.Time) (*attendance.WFHRequest, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE wfh_requests SET status = $3::wfh_status, decided_by = $4, decided_at = $5, decision_note = NULLIF($6, '')
		WHERE id = $1 AND team_id = $2 AND status = 'pending'`,
		id, teamID, status, deciderID, at.UTC(), note)
	if err != nil {
		return nil, err
	}
	cur, err := s.GetWFH(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, errors.New("wfh request disappeared")
	}
	if tag.RowsAffected() == 0 {
		return cur, attendance.ErrAlreadyDecided
	}
	return cur, nil
}

func (s *Store) CancelWFH(ctx context.Context, id int64, userID, today string) (*attendance.WFHRequest, error) {
	d, err := dateParam(today)
	if err != nil {
		return nil, err
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE wfh_requests SET status = 'cancelled'
		WHERE id = $1 AND mm_user_id = $2
		  AND (status = 'pending' OR (status = 'approved' AND start_date > $3))`, id, userID, d)
	if err != nil || tag.RowsAffected() == 0 {
		return nil, err
	}
	return s.GetWFH(ctx, id)
}

func (s *Store) WFHApprovedOn(ctx context.Context, userID, teamID, day string) (bool, error) {
	d, err := dateParam(day)
	if err != nil {
		return false, err
	}
	var ok bool
	err = s.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM wfh_requests WHERE mm_user_id = $1 AND team_id = $2
		               AND status = 'approved' AND NOT half_day AND start_date <= $3 AND end_date >= $3)`,
		userID, teamID, d).Scan(&ok)
	return ok, err
}

func (s *Store) ApprovedWFHDays(ctx context.Context, userID, teamID, fromDay, toDay string) (float64, error) {
	from, err := dateParam(fromDay)
	if err != nil {
		return 0, err
	}
	to, err := dateParam(toDay)
	if err != nil {
		return 0, err
	}
	var n float64
	err = s.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM((LEAST(end_date, $4::date) - GREATEST(start_date, $3::date) + 1)
		                    * CASE WHEN half_day THEN 0.5 ELSE 1 END), 0)::float8
		FROM wfh_requests WHERE mm_user_id = $1 AND team_id = $2 AND status = 'approved'
		  AND start_date <= $4 AND end_date >= $3`, userID, teamID, from, to).Scan(&n)
	return n, err
}

func (s *Store) SetOpenSessionMode(ctx context.Context, userID, teamID, mode string) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE sessions SET work_mode = $3
		WHERE mm_user_id = $1 AND team_id = $2 AND check_out IS NULL AND work_mode <> $3`, userID, teamID, mode)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
