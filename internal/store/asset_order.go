package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

// orderedAssetSiblings returns the assets a user orders inside one group (or the root when groupID is nil), in
// display order: the built-in local asset, fleet device rows and deleted rows have no place in that order.
func (s *Store) orderedAssetSiblings(ctx context.Context, db *gorm.DB, groupID *string, excludeID string) ([]AssetRow, error) {
	asset := query.Use(db).AssetRow
	q := asset.WithContext(ctx).
		Where(asset.DeletedAt.IsNull(), asset.Builtin.Is(false), asset.Kind.Neq(AssetKindDevice), asset.ID.Neq(excludeID)).
		Where(ownerConditions(ctx, asset.TableName())...)
	if groupID == nil {
		q = q.Where(asset.GroupID.IsNull())
	} else {
		q = q.Where(asset.GroupID.Eq(*groupID))
	}
	rows := []AssetRow{}
	if err := q.Order(asset.Sort, asset.Name).Scan(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// AssetTopSort is the sort value that places a new asset first in its group, above every asset already there.
func (s *Store) AssetTopSort(ctx context.Context, groupID *string) (int64, error) {
	siblings, err := s.orderedAssetSiblings(ctx, s.db, groupID, "")
	if err != nil {
		return 0, dbError(err)
	}
	if len(siblings) == 0 {
		return 0, nil
	}
	return siblings[0].Sort - 1, nil
}

// AssetMove places an asset in a group (nil for the root) directly before beforeID, or last when beforeID is
// empty, and renumbers that group's order. Assets the caller does not see in its list (hidden or outside the
// current project) keep their relative places, because the order is computed here from every sibling.
func (s *Store) AssetMove(ctx context.Context, id string, groupID *string, beforeID string) (AssetRow, error) {
	if beforeID == id {
		return AssetRow{}, badParam(errString("资产不能排在自己前面"))
	}
	var moved AssetRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := s.assetGet(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Builtin || current.Kind == AssetKindDevice {
			return badParam(errString("该资产不能调整顺序"))
		}
		if groupID != nil {
			if _, err := s.groupReference(ctx, tx, *groupID); err != nil {
				return err
			}
		}
		siblings, err := s.orderedAssetSiblings(ctx, tx, groupID, id)
		if err != nil {
			return err
		}
		position := len(siblings)
		if beforeID != "" {
			position = -1
			for index, row := range siblings {
				if row.ID == beforeID {
					position = index
					break
				}
			}
			if position < 0 {
				return badParam(errString("目标位置已变化，请刷新后重试"))
			}
		}
		ordered := make([]AssetRow, 0, len(siblings)+1)
		ordered = append(ordered, siblings[:position]...)
		ordered = append(ordered, current)
		ordered = append(ordered, siblings[position:]...)
		sameGroup := (current.GroupID == nil && groupID == nil) || (current.GroupID != nil && groupID != nil && *current.GroupID == *groupID)
		for index, row := range ordered {
			sort := int64(index)
			patch := AssetPatch{}
			if row.Sort != sort {
				patch.Sort = &sort
			}
			if row.ID == id && !sameGroup {
				if groupID == nil {
					patch.GroupID = Null[string]()
				} else {
					patch.GroupID = Value(*groupID)
				}
			}
			if patch.Sort == nil && !patch.GroupID.Set {
				continue
			}
			if _, err := s.assetUpdate(ctx, tx, row.ID, patch); err != nil {
				return err
			}
		}
		moved, err = s.assetGet(ctx, tx, id)
		return err
	})
	if err != nil {
		return AssetRow{}, dbError(err)
	}
	return moved, nil
}

// GroupMove places a group directly before beforeID among the groups that share its parent, or last when
// beforeID is empty, and renumbers that order. Changing the parent stays with GroupUpdate.
func (s *Store) GroupMove(ctx context.Context, id string, beforeID string) (AssetGroupRow, error) {
	if beforeID == id {
		return AssetGroupRow{}, badParam(errString("分组不能排在自己前面"))
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		group := query.Use(tx).AssetGroupRow
		current, err := group.WithContext(ctx).
			Where(group.ID.Eq(id)).
			Where(ownerConditions(ctx, group.TableName())...).
			First()
		if isNoRows(err) {
			return notFound("分组")
		}
		if err != nil {
			return err
		}
		q := group.WithContext(ctx).Where(group.ID.Neq(id)).Where(ownerConditions(ctx, group.TableName())...)
		if current.ParentID == nil {
			q = q.Where(group.ParentID.IsNull())
		} else {
			q = q.Where(group.ParentID.Eq(*current.ParentID))
		}
		siblings := []AssetGroupRow{}
		if err := q.Order(group.Sort, group.Name).Scan(&siblings); err != nil {
			return err
		}
		position := len(siblings)
		if beforeID != "" {
			position = -1
			for index, row := range siblings {
				if row.ID == beforeID {
					position = index
					break
				}
			}
			if position < 0 {
				return badParam(errString("目标位置已变化，请刷新后重试"))
			}
		}
		ordered := make([]AssetGroupRow, 0, len(siblings)+1)
		ordered = append(ordered, siblings[:position]...)
		ordered = append(ordered, *current)
		ordered = append(ordered, siblings[position:]...)
		now := ids.NowMS()
		for index, row := range ordered {
			if row.Sort == int64(index) {
				continue
			}
			if _, err := group.WithContext(ctx).
				Where(group.ID.Eq(row.ID)).
				Where(ownerConditions(ctx, group.TableName())...).
				UpdateColumns(map[string]any{"sort": int64(index), "updated_at": now}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return AssetGroupRow{}, dbError(err)
	}
	return s.GroupGet(ctx, id)
}
