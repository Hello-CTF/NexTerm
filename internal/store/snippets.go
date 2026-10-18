package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) SnippetList(ctx context.Context) ([]SnippetRow, error) {
	snippet := query.Use(s.db).SnippetRow
	result := []SnippetRow{}
	err := snippet.WithContext(ctx).
		Where(ownerConditions(ctx, snippet.TableName())...).
		Order(snippet.GroupID, snippet.Sort, snippet.Name, snippet.ID).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) SnippetCreate(ctx context.Context, name, body string, groupID *string, sort int64) (SnippetRow, error) {
	if groupID != nil {
		if _, err := s.groupReference(ctx, s.db, *groupID); err != nil {
			return SnippetRow{}, err
		}
	}
	id := ids.New()
	now := ids.NowMS()
	row := SnippetRow{ID: id, GroupID: groupID, Name: name, Body: body, Sort: sort, CreatedAt: now, UpdatedAt: now, OwnerID: ipc.OwnerID(ctx)}
	if err := query.Use(s.db).SnippetRow.WithContext(ctx).Create(&row); err != nil {
		return SnippetRow{}, dbError(err)
	}
	return s.SnippetGet(ctx, id)
}

func (s *Store) SnippetGet(ctx context.Context, id string) (SnippetRow, error) {
	snippet := query.Use(s.db).SnippetRow
	row, err := snippet.WithContext(ctx).
		Where(snippet.ID.Eq(id)).
		Where(ownerConditions(ctx, snippet.TableName())...).
		First()
	if isNoRows(err) {
		return SnippetRow{}, notFound("片段")
	}
	if err != nil {
		return SnippetRow{}, dbError(err)
	}
	return *row, nil
}

func (s *Store) SnippetUpdate(ctx context.Context, id, name, body string) error {
	snippet := query.Use(s.db).SnippetRow
	_, err := snippet.WithContext(ctx).
		Where(snippet.ID.Eq(id)).
		Where(ownerConditions(ctx, snippet.TableName())...).
		UpdateColumns(map[string]any{"name": name, "body": body, "updated_at": ids.NowMS()})
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
	if patch.GroupID.Set && patch.GroupID.Value != nil {
		if _, err := s.groupReference(ctx, s.db, *patch.GroupID.Value); err != nil {
			return err
		}
	}
	row.Name = patch.Name
	row.Body = patch.Body
	applyOptional(&row.GroupID, patch.GroupID)
	if patch.Sort != nil {
		row.Sort = *patch.Sort
	}
	snippet := query.Use(s.db).SnippetRow
	_, err = snippet.WithContext(ctx).
		Where(snippet.ID.Eq(id)).
		Where(ownerConditions(ctx, snippet.TableName())...).
		UpdateColumns(map[string]any{"name": row.Name, "body": row.Body, "group_id": row.GroupID, "sort": row.Sort, "updated_at": ids.NowMS()})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) SnippetDelete(ctx context.Context, id string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		snippet := queries.SnippetRow
		if _, err := snippet.WithContext(ctx).
			Where(snippet.ID.Eq(id)).
			Where(ownerConditions(ctx, snippet.TableName())...).
			Delete(); err != nil {
			return err
		}
		tombstone := queries.SyncTombstone
		return tombstone.WithContext(ctx).
			Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}}, DoNothing: true}).
			Create(&SyncTombstone{ID: id, Kind: "snippet", DeletedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)})
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}
