package store

import (
	"context"
)

// CommandLogRow is one audited command as stored in command_log.
type CommandLogRow struct {
	ID         int64   `json:"id"`
	SessionID  string  `json:"sessionId"`
	TabID      string  `json:"tabId"`
	AssetID    string  `json:"assetId"`
	UserID     *string `json:"userId"`
	Command    string  `json:"command"`
	Source     string  `json:"source"`
	ExitCode   *int32  `json:"exitCode"`
	StartedAt  int64   `json:"startedAt"`
	FinishedAt int64   `json:"finishedAt"`
}

// CommandLogInput is one command to append to the command log. An empty
// UserID stores NULL (desktop and background sessions carry no account user).
type CommandLogInput struct {
	SessionID  string
	TabID      string
	AssetID    string
	UserID     string
	Command    string
	Source     string
	ExitCode   *int
	StartedAt  int64
	FinishedAt int64
}

// CommandLogQuery filters command log reads; nil fields match everything.
type CommandLogQuery struct {
	SessionID *string
	AssetID   *string
	UserID    *string
	Limit     int64
	Offset    int64
}

func (s *Store) CommandLogInsert(ctx context.Context, input CommandLogInput) error {
	var userID *string
	if input.UserID != "" {
		userID = &input.UserID
	}
	var exitCode *int32
	if input.ExitCode != nil {
		code := int32(*input.ExitCode)
		exitCode = &code
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO command_log(session_id, tab_id, asset_id, user_id, command, source, exit_code, started_at, finished_at)
VALUES(?,?,?,?,?,?,?,?,?)`, input.SessionID, input.TabID, input.AssetID, userID, input.Command, input.Source, exitCode, input.StartedAt, input.FinishedAt)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) CommandLogCount(ctx context.Context, query CommandLogQuery) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM command_log
WHERE (CAST(? AS TEXT) IS NULL OR session_id = ?) AND (CAST(? AS TEXT) IS NULL OR asset_id = ?)
AND (CAST(? AS TEXT) IS NULL OR user_id = ?)`,
		query.SessionID, query.SessionID, query.AssetID, query.AssetID, query.UserID, query.UserID).Scan(&count)
	if err != nil {
		return 0, dbError(err)
	}
	return count, nil
}

func (s *Store) CommandLogQuery(ctx context.Context, query CommandLogQuery) ([]CommandLogRow, error) {
	limit := query.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, tab_id, asset_id, user_id, command, source, exit_code, started_at, finished_at
FROM command_log
WHERE (CAST(? AS TEXT) IS NULL OR session_id = ?) AND (CAST(? AS TEXT) IS NULL OR asset_id = ?)
AND (CAST(? AS TEXT) IS NULL OR user_id = ?)
ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`, query.SessionID, query.SessionID,
		query.AssetID, query.AssetID, query.UserID, query.UserID, limit, query.Offset)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []CommandLogRow{}
	for rows.Next() {
		var row CommandLogRow
		if err := rows.Scan(&row.ID, &row.SessionID, &row.TabID, &row.AssetID, &row.UserID,
			&row.Command, &row.Source, &row.ExitCode, &row.StartedAt, &row.FinishedAt); err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
