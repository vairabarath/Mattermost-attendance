package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

const memberCols = `mm_user_id, username, COALESCE(display_name, ''), expected_hours::float8, reminders_on, tracked, active, created_at`

func scanMember(r pgx.Row) (attendance.MemberSettings, error) {
	var m attendance.MemberSettings
	err := r.Scan(&m.UserID, &m.Username, &m.DisplayName, &m.ExpectedHours, &m.RemindersOn, &m.Tracked, &m.Active, &m.Since)
	return m, err
}

func (s *Store) GetMember(ctx context.Context, userID string) (*attendance.MemberSettings, error) {
	m, err := scanMember(s.Pool.QueryRow(ctx, `SELECT `+memberCols+` FROM members WHERE mm_user_id = $1`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// UpdateMember applies the non-nil fields and returns the settings before and after.
func (s *Store) UpdateMember(ctx context.Context, userID string, upd attendance.MemberUpdate) (before, after attendance.MemberSettings, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return before, after, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	before, err = scanMember(tx.QueryRow(ctx, `SELECT `+memberCols+` FROM members WHERE mm_user_id = $1 FOR UPDATE`, userID))
	if err != nil {
		return before, after, err
	}
	after = before
	if upd.Tracked != nil {
		after.Tracked = *upd.Tracked
	}
	if upd.RemindersOn != nil {
		after.RemindersOn = *upd.RemindersOn
	}
	if upd.ExpectedHours != nil {
		after.ExpectedHours = *upd.ExpectedHours
	}
	if _, err = tx.Exec(ctx, `UPDATE members SET tracked = $2, reminders_on = $3, expected_hours = $4 WHERE mm_user_id = $1`,
		userID, after.Tracked, after.RemindersOn, after.ExpectedHours); err != nil {
		return before, after, err
	}
	return before, after, tx.Commit(ctx)
}

func (s *Store) InsertAudit(ctx context.Context, e attendance.AuditEntry) error {
	toJSON := func(v any) ([]byte, error) {
		if v == nil {
			return nil, nil
		}
		return json.Marshal(v)
	}
	oldV, err := toJSON(e.Old)
	if err != nil {
		return err
	}
	newV, err := toJSON(e.New)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO audit_log (actor_id, target_id, team_id, action, old_value, new_value, reason)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, NULLIF($7, ''))`,
		e.ActorID, e.TargetID, e.TeamID, e.Action, oldV, newV, e.Reason)
	return err
}

// dateParam turns YYYY-MM-DD into a value pgx encodes as DATE.
func dateParam(day string) (time.Time, error) {
	return time.Parse(attendance.DayFormat, day)
}

func (s *Store) AddHoliday(ctx context.Context, day, teamID, name string) (int64, error) {
	d, err := dateParam(day)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.Pool.QueryRow(ctx, `
		INSERT INTO holidays (day, team_id, name) VALUES ($1, NULLIF($2, ''), $3) RETURNING id`,
		d, teamID, name).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return 0, attendance.ErrHolidayExists
	}
	return id, err
}

const holidayCols = `id, to_char(day, 'YYYY-MM-DD'), COALESCE(team_id, ''), name`

func collectHolidays(rows pgx.Rows, err error) ([]attendance.Holiday, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (attendance.Holiday, error) {
		var h attendance.Holiday
		err := r.Scan(&h.ID, &h.Day, &h.TeamID, &h.Name)
		return h, err
	})
}

func (s *Store) ListHolidays(ctx context.Context, teamID, fromDay string) ([]attendance.Holiday, error) {
	d, err := dateParam(fromDay)
	if err != nil {
		return nil, err
	}
	return collectHolidays(s.Pool.Query(ctx, `
		SELECT `+holidayCols+` FROM holidays
		WHERE day >= $2 AND (team_id IS NULL OR team_id = $1)
		ORDER BY day, id`, teamID, d))
}

func (s *Store) HolidaysOn(ctx context.Context, day string) ([]attendance.Holiday, error) {
	d, err := dateParam(day)
	if err != nil {
		return nil, err
	}
	return collectHolidays(s.Pool.Query(ctx, `SELECT `+holidayCols+` FROM holidays WHERE day = $1`, d))
}

func (s *Store) GetHoliday(ctx context.Context, id int64) (*attendance.Holiday, error) {
	hs, err := collectHolidays(s.Pool.Query(ctx, `SELECT `+holidayCols+` FROM holidays WHERE id = $1`, id))
	if err != nil || len(hs) == 0 {
		return nil, err
	}
	return &hs[0], nil
}

func (s *Store) DeleteHoliday(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM holidays WHERE id = $1`, id)
	return err
}
