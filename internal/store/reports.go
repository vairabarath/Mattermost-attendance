package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

func (s *Store) HolidaysBetween(ctx context.Context, fromDay, toDay string) ([]attendance.Holiday, error) {
	from, err := dateParam(fromDay)
	if err != nil {
		return nil, err
	}
	to, err := dateParam(toDay)
	if err != nil {
		return nil, err
	}
	return collectHolidays(s.Pool.Query(ctx, `
		SELECT `+holidayCols+` FROM holidays WHERE day BETWEEN $1 AND $2 ORDER BY day, id`, from, to))
}

// auditSession is how sessions are stored in audit_log.
type auditSession struct {
	ID       int64              `json:"id,omitempty"`
	TeamID   string             `json:"team_id"`
	CheckIn  time.Time          `json:"check_in"`
	CheckOut *time.Time         `json:"check_out"`
	WorkMode string             `json:"work_mode"`
	Auto     bool               `json:"auto_closed,omitempty"`
	Breaks   []attendance.Break `json:"breaks,omitempty"`
}

// FixDay implements `/att fix` in one transaction (see attendance.ReportStore).
func (s *Store) FixDay(ctx context.Context, r attendance.FixRequest) (attendance.FixResult, error) {
	var res attendance.FixResult
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Serialize fixes and check-ins for this member.
	if _, err = tx.Exec(ctx, `SELECT 1 FROM members WHERE mm_user_id = $1 FOR UPDATE`, r.UserID); err != nil {
		return res, err
	}

	var open bool
	if err = tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM sessions WHERE mm_user_id = $1 AND team_id = $2
		               AND check_out IS NULL AND check_in >= $3 AND check_in < $4)`,
		r.UserID, r.TeamID, r.DayStart.UTC(), r.DayEnd.UTC()).Scan(&open); err != nil {
		return res, err
	}
	if open {
		return res, attendance.ErrOpenSessionOnDay
	}

	rows, err := tx.Query(ctx, `
		SELECT id, team_id, check_in, check_out, work_mode, auto_closed FROM sessions
		WHERE mm_user_id = $1 AND team_id = $2 AND check_in >= $3 AND check_in < $4
		ORDER BY check_in FOR UPDATE`, r.UserID, r.TeamID, r.DayStart.UTC(), r.DayEnd.UTC())
	if err != nil {
		return res, err
	}
	res.Old, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (attendance.Session, error) {
		var x attendance.Session
		err := row.Scan(&x.ID, &x.TeamID, &x.CheckIn, &x.CheckOut, &x.WorkMode, &x.AutoClosed)
		return x, err
	})
	if err != nil {
		return res, err
	}
	ids := make([]int64, len(res.Old))
	for i, x := range res.Old {
		ids[i] = x.ID
	}
	breaks, err := loadBreaks(ctx, tx, ids)
	if err != nil {
		return res, err
	}
	for i := range res.Old {
		res.Old[i].Breaks = breaks[res.Old[i].ID]
	}

	// The new span must not overlap any session that stays (other teams,
	// or a session from the previous day running past midnight).
	var overlap bool
	if err = tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM sessions WHERE mm_user_id = $1 AND NOT (id = ANY($2))
		               AND check_in < $4 AND (check_out IS NULL OR check_out > $3))`,
		r.UserID, ids, r.In.UTC(), r.Out.UTC()).Scan(&overlap); err != nil {
		return res, err
	}
	if overlap {
		return res, attendance.ErrFixOverlap
	}

	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE id = ANY($1)`, ids); err != nil {
		return res, err
	}
	if err = tx.QueryRow(ctx, `
		INSERT INTO sessions (mm_user_id, team_id, check_in, check_out, work_mode)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		r.UserID, r.TeamID, r.In.UTC(), r.Out.UTC(), r.Mode).Scan(&res.NewID); err != nil {
		return res, err
	}

	old := make([]auditSession, len(res.Old))
	for i, x := range res.Old {
		old[i] = auditSession{x.ID, x.TeamID, x.CheckIn, x.CheckOut, x.WorkMode, x.AutoClosed, x.Breaks}
	}
	out := r.Out.UTC()
	oldJSON, err := json.Marshal(old)
	if err != nil {
		return res, err
	}
	newJSON, err := json.Marshal(auditSession{ID: res.NewID, TeamID: r.TeamID, CheckIn: r.In.UTC(), CheckOut: &out, WorkMode: r.Mode})
	if err != nil {
		return res, err
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO audit_log (actor_id, target_id, team_id, action, old_value, new_value, reason)
		VALUES ($1, $2, $3, 'fix', $4, $5, $6)`,
		r.ActorID, r.UserID, r.TeamID, oldJSON, newJSON, r.Reason); err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
}

func (s *Store) TeamSessionsBetween(ctx context.Context, teamID string, from, to time.Time) ([]attendance.ExportRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT m.username, COALESCE(m.display_name, ''), s.id, s.team_id, s.check_in, s.check_out,
		       s.work_mode, s.auto_closed, COALESCE(s.in_note, ''), COALESCE(s.out_summary, '')
		FROM sessions s JOIN members m USING (mm_user_id)
		WHERE s.team_id = $1 AND s.check_in >= $2 AND s.check_in < $3
		ORDER BY m.username, s.check_in`, teamID, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (attendance.ExportRow, error) {
		var e attendance.ExportRow
		x := &e.Session
		err := r.Scan(&e.Username, &e.DisplayName, &x.ID, &x.TeamID, &x.CheckIn, &x.CheckOut,
			&x.WorkMode, &x.AutoClosed, &x.InNote, &x.OutSummary)
		return e, err
	})
	if err != nil || len(list) == 0 {
		return list, err
	}
	ids := make([]int64, len(list))
	for i, e := range list {
		ids[i] = e.Session.ID
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
