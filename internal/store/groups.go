package store

import (
	"context"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const groupColumns = "id, parent_id, name, sort, created_at, updated_at"

type rowScanner interface {
	Scan(dest ...any) error
}

func scanGroup(row rowScanner) (AssetGroupRow, error) {
	var result AssetGroupRow
	err := row.Scan(&result.ID, &result.ParentID, &result.Name, &result.Sort, &result.CreatedAt, &result.UpdatedAt)
	return result, err
}

func (s *Store) GroupList(ctx context.Context) ([]AssetGroupRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+groupColumns+" FROM asset_group ORDER BY sort, name")
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []AssetGroupRow{}
	for rows.Next() {
		row, err := scanGroup(rows)
		if err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) GroupGet(ctx context.Context, id string) (AssetGroupRow, error) {
	row, err := scanGroup(s.db.QueryRowContext(ctx, "SELECT "+groupColumns+" FROM asset_group WHERE id = ?", id))
	if isNoRows(err) {
		return AssetGroupRow{}, notFound("分组 " + id)
	}
	if err != nil {
		return AssetGroupRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) GroupCreate(ctx context.Context, input GroupInput) (AssetGroupRow, error) {
	id := ids.New()
	now := ids.NowMS()
	_, err := s.db.ExecContext(ctx, `INSERT INTO asset_group(id, parent_id, name, sort, created_at, updated_at)
VALUES(?,?,?,?,?,?)`, id, input.ParentID, input.Name, input.Sort, now, now)
	if err != nil {
		return AssetGroupRow{}, dbError(err)
	}
	return s.GroupGet(ctx, id)
}

func (s *Store) GroupUpdate(ctx context.Context, id string, patch GroupPatch) (AssetGroupRow, error) {
	row, err := s.GroupGet(ctx, id)
	if err != nil {
		return AssetGroupRow{}, err
	}
	if patch.Name != nil {
		row.Name = *patch.Name
	}
	if patch.ParentID.Set {
		row.ParentID = patch.ParentID.Value
	}
	if patch.Sort != nil {
		row.Sort = *patch.Sort
	}
	if row.ParentID != nil && *row.ParentID == id {
		return AssetGroupRow{}, badParam(errString("父级不能是自己"))
	}
	if row.ParentID != nil {
		cursor := *row.ParentID
		for depth := 0; depth <= 32; depth++ {
			var parent *string
			err := s.db.QueryRowContext(ctx, "SELECT parent_id FROM asset_group WHERE id = ?", cursor).Scan(&parent)
			if isNoRows(err) {
				break
			}
			if err != nil {
				return AssetGroupRow{}, dbError(err)
			}
			if parent == nil {
				break
			}
			if *parent == id {
				return AssetGroupRow{}, badParam(errString("不允许把分组移动到自己的后代下"))
			}
			cursor = *parent
		}
	}
	_, err = s.db.ExecContext(ctx, "UPDATE asset_group SET name=?, parent_id=?, sort=?, updated_at=? WHERE id=?",
		row.Name, row.ParentID, row.Sort, ids.NowMS(), id)
	if err != nil {
		return AssetGroupRow{}, dbError(err)
	}
	return s.GroupGet(ctx, id)
}

func (s *Store) GroupDelete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM asset_group WHERE id = ?", id)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) GroupUpsert(ctx context.Context, id string, parentID *string, name string, sort, createdAt, updatedAt int64) (bool, error) {
	if err := EnsureID(id); err != nil {
		return false, err
	}
	if strings.TrimSpace(name) == "" {
		return false, badParam(errString("分组名称不能为空"))
	}
	var ignored string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM asset_group WHERE id = ?", id).Scan(&ignored)
	exists := err == nil
	if err != nil && !isNoRows(err) {
		return false, dbError(err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO asset_group(id, parent_id, name, sort, created_at, updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET parent_id=excluded.parent_id, name=excluded.name,
sort=excluded.sort, updated_at=excluded.updated_at`,
		id, parentID, strings.TrimSpace(name), sort, createdAt, updatedAt)
	if err != nil {
		return false, dbError(err)
	}
	return !exists, nil
}

type errString string

func (e errString) Error() string { return string(e) }
