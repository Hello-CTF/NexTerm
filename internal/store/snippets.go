package store

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func scanSnippet(row rowScanner) (SnippetRow, error) {
	var result SnippetRow
	err := row.Scan(&result.ID, &result.GroupID, &result.Name, &result.Body, &result.Sort, &result.CreatedAt, &result.UpdatedAt)
	return result, err
}

func (s *Store) SnippetList(ctx context.Context) ([]SnippetRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, group_id, name, body, sort, created_at, updated_at FROM snippet ORDER BY sort, name")
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []SnippetRow{}
	for rows.Next() {
		row, err := scanSnippet(rows)
		if err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) SnippetCreate(ctx context.Context, name, body string, groupID *string, sort int64) (SnippetRow, error) {
	id := ids.New()
	now := ids.NowMS()
	_, err := s.db.ExecContext(ctx, `INSERT INTO snippet(id, group_id, name, body, sort, created_at, updated_at)
VALUES(?,?,?,?,?,?,?)`, id, groupID, name, body, sort, now, now)
	if err != nil {
		return SnippetRow{}, dbError(err)
	}
	return s.SnippetGet(ctx, id)
}

func (s *Store) SnippetGet(ctx context.Context, id string) (SnippetRow, error) {
	row, err := scanSnippet(s.db.QueryRowContext(ctx,
		"SELECT id, group_id, name, body, sort, created_at, updated_at FROM snippet WHERE id = ?", id))
	if isNoRows(err) {
		return SnippetRow{}, notFound("片段 " + id)
	}
	if err != nil {
		return SnippetRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) SnippetUpdate(ctx context.Context, id, name, body string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE snippet SET name=?, body=?, updated_at=? WHERE id=?",
		name, body, ids.NowMS(), id)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) SnippetDelete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM snippet WHERE id = ?", id)
	if err != nil {
		return dbError(err)
	}
	return nil
}
