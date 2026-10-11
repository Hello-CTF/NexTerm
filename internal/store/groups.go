package store

import (
	"context"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
			group := query.Use(s.db).AssetGroupRow
			parent, err := group.WithContext(ctx).
				Where(group.ID.Eq(cursor)).
				Where(ownerConditions(ctx, group.TableName())...).
				First()
			if isNoRows(err) {
				break
			}
			if err != nil {
				return AssetGroupRow{}, dbError(err)
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
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		group := queries.AssetGroupRow
		if _, err := group.WithContext(ctx).
			Where(group.ID.Eq(id)).
			Where(ownerConditions(ctx, group.TableName())...).
			Delete(); err != nil {
			return err
		}
		tombstone := queries.SyncTombstone
		return tombstone.WithContext(ctx).
			Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}}, DoNothing: true}).
			Create(&SyncTombstone{ID: id, Kind: "group", DeletedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)})
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

type errString string

func (e errString) Error() string { return string(e) }
