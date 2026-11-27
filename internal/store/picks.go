package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

var _ attendance.PickStore = (*Store)(nil)

func (s *Store) AddTaskPick(ctx context.Context, userID, teamID, day, issueURL string) error {
	d, err := dateParam(day)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO task_picks (mm_user_id, team_id, day, issue_url) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, userID, teamID, d, issueURL)
	return err
}

func (s *Store) TaskPicks(ctx context.Context, userID, teamID, day string) ([]string, error) {
	d, err := dateParam(day)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT issue_url FROM task_picks WHERE mm_user_id = $1 AND team_id = $2 AND day = $3
		ORDER BY picked_at`, userID, teamID, d)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
