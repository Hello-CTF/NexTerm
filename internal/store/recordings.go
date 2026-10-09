package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

func (s *Store) RecordingStart(ctx context.Context, sessionID, tabID, path string) (string, error) {
	id := ids.New()
	_, err := s.db.ExecContext(ctx, `INSERT INTO terminal_recording(id, session_id, tab_id, path, bytes, started_at)
VALUES(?,?,?,?,0,?)`, id, sessionID, tabID, path, ids.NowMS())
	if err != nil {
		return "", dbError(err)
	}
	return id, nil
}

func (s *Store) RecordingUpdateBytes(ctx context.Context, id string, bytes int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE terminal_recording SET bytes=? WHERE id=?", bytes, id)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) RecordingEnd(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE terminal_recording SET ended_at=? WHERE id=?", ids.NowMS(), id)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) RecordingList(ctx context.Context) ([]RecordingRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, tab_id, path, bytes, started_at, ended_at
FROM terminal_recording ORDER BY started_at DESC LIMIT 200`)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []RecordingRow{}
	for rows.Next() {
		var row RecordingRow
		if err := rows.Scan(&row.ID, &row.SessionID, &row.TabID, &row.Path, &row.Bytes,
			&row.StartedAt, &row.EndedAt); err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
