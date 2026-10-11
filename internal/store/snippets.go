package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func scanSnippet(row rowScanner) (SnippetRow, error) {
	var result SnippetRow
	err := row.Scan(&result.ID, &result.GroupID, &result.Name, &result.Body, &result.Sort, &result.CreatedAt, &result.UpdatedAt)
	return result, err
}

func (s *Store) SnippetList(ctx context.Context) ([]SnippetRow, error) {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	rows, err := s.db.QueryContext(ctx, "SELECT id, group_id, name, body, sort, created_at, updated_at FROM snippet WHERE 1=1"+condition+" ORDER BY group_id, sort, name, id", args...)
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO snippet(owner_id, id, group_id, name, body, sort, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?)`, ipc.OwnerID(ctx), id, groupID, name, body, sort, now, now)
	if err != nil {
		return SnippetRow{}, dbError(err)
	}
	return s.SnippetGet(ctx, id)
}

func (s *Store) SnippetGet(ctx context.Context, id string) (SnippetRow, error) {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	row, err := scanSnippet(s.db.QueryRowContext(ctx,
		"SELECT id, group_id, name, body, sort, created_at, updated_at FROM snippet WHERE id = ?"+condition, append([]any{id}, args...)...))
	if isNoRows(err) {
		return SnippetRow{}, notFound("片段")
	}
	if err != nil {
		return SnippetRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) SnippetUpdate(ctx context.Context, id, name, body string) error {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	_, err := s.db.ExecContext(ctx, "UPDATE snippet SET name=?, body=?, updated_at=? WHERE id = ?"+condition,
		append([]any{name, body, ids.NowMS(), id}, args...)...)
	if err != nil {
		return dbError(err)
	}
	return nil
}

type SnippetPatch struct {
	Name    string
	Body    string
	GroupID Optional[string]
	Sort    *int64
}

func (s *Store) SnippetUpdateFields(ctx context.Context, id string, patch SnippetPatch) error {
	row, err := s.SnippetGet(ctx, id)
	if err != nil {
		return err
	}
	row.Name = patch.Name
	row.Body = patch.Body
	applyOptional(&row.GroupID, patch.GroupID)
	if patch.Sort != nil {
		row.Sort = *patch.Sort
	}
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	_, err = s.db.ExecContext(ctx, "UPDATE snippet SET name=?, body=?, group_id=?, sort=?, updated_at=? WHERE id = ?"+condition,
		append([]any{row.Name, row.Body, row.GroupID, row.Sort, ids.NowMS(), id}, args...)...)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) SnippetDelete(ctx context.Context, id string) error {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	_, err := s.db.ExecContext(ctx, "DELETE FROM snippet WHERE id = ?"+condition, append([]any{id}, args...)...)
	if err != nil {
		return dbError(err)
	}
	return nil
}
