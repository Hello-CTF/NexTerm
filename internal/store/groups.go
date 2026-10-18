package store

import (
	"context"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) GroupList(ctx context.Context) ([]AssetGroupRow, error) {
	group := query.Use(s.db).AssetGroupRow
	result := []AssetGroupRow{}
	err := group.WithContext(ctx).
		Where(ownerConditions(ctx, group.TableName())...).
		Order(group.Sort, group.Name).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) GroupGet(ctx context.Context, id string) (AssetGroupRow, error) {
	group := query.Use(s.db).AssetGroupRow
	row, err := group.WithContext(ctx).
		Where(group.ID.Eq(id)).
		Where(ownerConditions(ctx, group.TableName())...).
		First()
	if isNoRows(err) {
		return AssetGroupRow{}, notFound("分组")
	}
	if err != nil {
		return AssetGroupRow{}, dbError(err)
	}
	return *row, nil
}

func (s *Store) GroupCreate(ctx context.Context, input GroupInput) (AssetGroupRow, error) {
	if input.ParentID != nil {
		if _, err := s.groupReference(ctx, s.db, *input.ParentID); err != nil {
			return AssetGroupRow{}, err
		}
	}
	id := ids.New()
	now := ids.NowMS()
	row := AssetGroupRow{ID: id, ParentID: input.ParentID, Name: input.Name, Sort: input.Sort, CreatedAt: now, UpdatedAt: now, OwnerID: ipc.OwnerID(ctx)}
	if err := query.Use(s.db).AssetGroupRow.WithContext(ctx).Create(&row); err != nil {
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
			parent, err := s.groupReference(ctx, s.db, cursor)
			if err != nil {
				return AssetGroupRow{}, err
			}
			if parent.ParentID == nil {
				break
			}
			if *parent.ParentID == id {
				return AssetGroupRow{}, badParam(errString("不允许将分组移动到其后代分组下"))
			}
			cursor = *parent.ParentID
		}
	}
	group := query.Use(s.db).AssetGroupRow
	_, err = group.WithContext(ctx).
		Where(group.ID.Eq(id)).
		Where(ownerConditions(ctx, group.TableName())...).
		UpdateColumns(map[string]any{"name": row.Name, "parent_id": row.ParentID, "sort": row.Sort, "updated_at": ids.NowMS()})
	if err != nil {
		return AssetGroupRow{}, dbError(err)
	}
	return s.GroupGet(ctx, id)
}

func (s *Store) GroupDelete(ctx context.Context, id string) error {
	return s.GroupDeleteAt(ctx, id, ids.NowMS())
}

// GroupDeleteAt removes a group and records deletion revisions for every group
// removed by the parent cascade, so their sync objects cannot reappear later.
func (s *Store) GroupDeleteAt(ctx context.Context, id string, deletedAt int64) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		group := queries.AssetGroupRow
		rows := []AssetGroupRow{}
		if err := group.WithContext(ctx).
			Where(ownerConditions(ctx, group.TableName())...).
			Scan(&rows); err != nil {
			return err
		}
		children := make(map[string][]string, len(rows))
		revisions := make(map[string]int64, len(rows))
		for _, row := range rows {
			revisions[row.ID] = row.UpdatedAt
			if row.ParentID != nil {
				children[*row.ParentID] = append(children[*row.ParentID], row.ID)
			}
		}
		if _, owned := revisions[id]; !owned {
			return notFound("分组")
		}
		stack := []string{id}
		seen := make(map[string]bool)
		removed := make([]string, 0)
		for len(stack) > 0 {
			last := len(stack) - 1
			groupID := stack[last]
			stack = stack[:last]
			if seen[groupID] {
				continue
			}
			seen[groupID] = true
			removed = append(removed, groupID)
			stack = append(stack, children[groupID]...)
		}
		if _, err := group.WithContext(ctx).
			Where(group.ParentID.In(removed...)).
			Where(group.OwnerID.Neq(ipc.OwnerID(ctx))).
			UpdateColumns(map[string]any{"parent_id": nil, "updated_at": MaxExpression(tx, "", "updated_at", deletedAt)}); err != nil {
			return err
		}
		asset := queries.AssetRow
		if _, err := asset.WithContext(ctx).
			Where(asset.GroupID.In(removed...)).
			UpdateColumns(map[string]any{"group_id": nil, "updated_at": MaxExpression(tx, "", "updated_at", deletedAt)}); err != nil {
			return err
		}
		snippet := queries.SnippetRow
		if _, err := snippet.WithContext(ctx).
			Where(snippet.GroupID.In(removed...)).
			UpdateColumns(map[string]any{"group_id": nil, "updated_at": MaxExpression(tx, "", "updated_at", deletedAt)}); err != nil {
			return err
		}
		if _, err := group.WithContext(ctx).
			Where(group.ID.Eq(id)).
			Where(ownerConditions(ctx, group.TableName())...).
			Delete(); err != nil {
			return err
		}
		for _, groupID := range removed {
			revision := deletedAt
			if revisions[groupID] > revision {
				revision = revisions[groupID]
			}
			if err := s.syncTombstonePut(ctx, tx, groupID, "group", revision); err != nil {
				return err
			}
		}
		return nil
	})
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
	if parentID != nil {
		if _, err := s.groupReference(ctx, s.db, *parentID); err != nil {
			return false, err
		}
	}
	group := query.Use(s.db).AssetGroupRow
	_, err := group.WithContext(ctx).
		Where(group.ID.Eq(id)).
		Where(ownerConditions(ctx, group.TableName())...).
		First()
	exists := err == nil
	if err != nil && !isNoRows(err) {
		return false, dbError(err)
	}
	row := AssetGroupRow{ID: id, ParentID: parentID, Name: strings.TrimSpace(name), Sort: sort, CreatedAt: createdAt, UpdatedAt: updatedAt, OwnerID: ipc.OwnerID(ctx)}
	if err := s.upsert(ctx, &row, []string{"parent_id", "name", "sort", "updated_at"}); err != nil {
		return false, err
	}
	return !exists, nil
}

func (s *Store) groupReference(ctx context.Context, db *gorm.DB, id string) (AssetGroupRow, error) {
	if id == "" {
		return AssetGroupRow{}, badParam(errString("分组 ID 不能为空"))
	}
	group := query.Use(db).AssetGroupRow
	row, err := group.WithContext(ctx).Where(group.ID.Eq(id)).First()
	if isNoRows(err) {
		return AssetGroupRow{}, badParam(errString("分组不存在"))
	}
	if err != nil {
		return AssetGroupRow{}, dbError(err)
	}
	if row.OwnerID != ipc.OwnerID(ctx) {
		return AssetGroupRow{}, ipc.NewError(ipc.CodeForbidden, "分组不属于当前用户")
	}
	return *row, nil
}

type errString string

func (e errString) Error() string { return string(e) }
