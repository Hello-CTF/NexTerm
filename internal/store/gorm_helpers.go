package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"gorm.io/gen"
	"gorm.io/gen/field"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func ownerConditions(ctx context.Context, table string) []gen.Condition {
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		return []gen.Condition{field.NewString(table, "owner_id").Eq(ownerID)}
	}
	return nil
}

func (s *Store) upsert(ctx context.Context, values any, columns []string) error {
	return s.upsertWithDB(ctx, s.db, values, columns)
}

func (s *Store) upsertWithDB(ctx context.Context, db *gorm.DB, values any, columns []string) error {
	err := db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns(columns),
		Where: clause.Where{Exprs: []clause.Expression{clause.Eq{
			Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx),
		}}},
	}).Create(values).Error
	if err != nil {
		return dbError(err)
	}
	return nil
}
