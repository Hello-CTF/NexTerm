package store

import (
	"context"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
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
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	rows, err := s.db.QueryContext(ctx, "SELECT "+groupColumns+" FROM asset_group WHERE 1=1"+condition+" ORDER BY sort, name", args...)
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
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	row, err := scanGroup(s.db.QueryRowContext(ctx, "SELECT "+groupColumns+" FROM asset_group WHERE id = ?"+condition, append([]any{id}, args...)...))
	if isNoRows(err) {
		return AssetGroupRow{}, notFound("分组")
	}
	if err != nil {
		return AssetGroupRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) GroupCreate(ctx context.Context, input GroupInput) (AssetGroupRow, error) {
	id := ids.New()
	now := ids.NowMS()
	_, err := s.db.ExecContext(ctx, `INSERT INTO asset_group(owner_id, id, parent_id, name, sort, created_at, updated_at)
VALUES(?,?,?,?,?,?,?)`, ipc.OwnerID(ctx), id, input.ParentID, input.Name, input.Sort, now, now)
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
		return AssetGroupRow{}, badParam(errString("分组不能将自身设为父级"))
	}
	if row.ParentID != nil {
		cursor := *row.ParentID
		for depth := 0; depth <= 32; depth++ {
			var parent *string
			parentCondition, parentArgs := ipc.OwnerCondition(ctx, "owner_id")
			err := s.db.QueryRowContext(ctx, "SELECT parent_id FROM asset_group WHERE id = ?"+parentCondition, append([]any{cursor}, parentArgs...)...).Scan(&parent)
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
				return AssetGroupRow{}, badParam(errString("不允许将分组移动到其后代分组下"))
			}
			cursor = *parent
		}
	}
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	_, err = s.db.ExecContext(ctx, "UPDATE asset_group SET name=?, parent_id=?, sort=?, updated_at=? WHERE id = ?"+condition,
		append([]any{row.Name, row.ParentID, row.Sort, ids.NowMS(), id}, args...)...)
	if err != nil {
		return AssetGroupRow{}, dbError(err)
	}
	return s.GroupGet(ctx, id)
}

func (s *Store) GroupDelete(ctx context.Context, id string) error {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	_, err := s.db.ExecContext(ctx, "DELETE FROM asset_group WHERE id = ?"+condition, append([]any{id}, args...)...)
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
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	var ignored string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM asset_group WHERE id = ?"+condition, append([]any{id}, args...)...).Scan(&ignored)
	exists := err == nil
	if err != nil && !isNoRows(err) {
		return false, dbError(err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO asset_group(owner_id, id, parent_id, name, sort, created_at, updated_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET parent_id=excluded.parent_id, name=excluded.name,
sort=excluded.sort, updated_at=excluded.updated_at
WHERE owner_id = ?`, ipc.OwnerID(ctx), id, parentID, strings.TrimSpace(name), sort, createdAt, updatedAt, ipc.OwnerID(ctx))
	if err != nil {
		return false, dbError(err)
	}
	return !exists, nil
}

type errString string

func (e errString) Error() string { return string(e) }
