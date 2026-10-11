package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CredentialTombstone = model.CredentialTombstone

func (s *Store) CredentialTombstoneList(ctx context.Context) ([]CredentialTombstone, error) {
	tombstone := query.Use(s.db).CredentialTombstone
	result := []CredentialTombstone{}
	err := tombstone.WithContext(ctx).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		Order(tombstone.ID).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) CredentialTombstoneGet(ctx context.Context, id string) (CredentialTombstone, error) {
	tombstone := query.Use(s.db).CredentialTombstone
	row, err := tombstone.WithContext(ctx).
		Where(tombstone.ID.Eq(id)).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		First()
	if isNoRows(err) {
		return CredentialTombstone{}, notFound("凭据墓碑")
	}
	if err != nil {
		return CredentialTombstone{}, dbError(err)
	}
	return *row, nil
}

func (s *Store) CredentialTombstonePut(ctx context.Context, id string, deletedAt int64) error {
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"deleted_at": gorm.Expr(s.dialect.ScalarMax() + "(credential_tombstone.deleted_at, excluded.deleted_at)"),
			}),
			Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)}}},
		}).
		Create(&CredentialTombstone{ID: id, DeletedAt: deletedAt, OwnerID: ipc.OwnerID(ctx)}).Error
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) CredentialTombstoneClear(ctx context.Context, id string) error {
	tombstone := query.Use(s.db).CredentialTombstone
	_, err := tombstone.WithContext(ctx).
		Where(tombstone.ID.Eq(id)).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		Delete()
	if err != nil {
		return dbError(err)
	}
	return nil
}
