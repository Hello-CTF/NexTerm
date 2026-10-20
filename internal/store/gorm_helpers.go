package store

import (
	"context"
	"fmt"
	"reflect"

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
	return UpsertOwnerGuarded(ctx, db, values, columns)
}

// UpsertOwnerGuarded inserts values by primary key, falling back to an owner-guarded UPDATE
// when the row already exists. clause.OnConflict's DO UPDATE ... WHERE guard is silently
// dropped by the MySQL/MariaDB dialect (rendered as ON DUPLICATE KEY UPDATE without WHERE),
// so this portable shape is used instead: a cross-owner ID collision surfaces as
// ErrWriteConflict on every backend instead of overwriting the other owner's row
// (MySQL/MariaDB) or silently skipping the write (SQLite/PostgreSQL). Re-applying an
// identical row stays a successful no-op on all backends.
func UpsertOwnerGuarded(ctx context.Context, db *gorm.DB, values any, columns []string) error {
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(values); err != nil {
		return dbError(err)
	}
	if statement.Schema == nil || len(statement.Schema.PrimaryFields) != 1 {
		return dbError(fmt.Errorf("owner-guarded upsert requires exactly one primary key field"))
	}
	primary := statement.Schema.PrimaryFields[0]
	elem := reflect.ValueOf(values)
	for elem.Kind() == reflect.Pointer {
		elem = elem.Elem()
	}
	id, zero := primary.ValueOf(ctx, elem)
	if zero {
		return dbError(fmt.Errorf("owner-guarded upsert requires a non-zero primary key"))
	}
	assignments := make(map[string]any, len(columns))
	for _, column := range columns {
		field := statement.Schema.LookUpField(column)
		if field == nil {
			return dbError(fmt.Errorf("owner-guarded upsert column %s not found in schema", column))
		}
		value, _ := field.ValueOf(ctx, elem)
		assignments[column] = value
	}
	tx := db.WithContext(ctx)
	inserted := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: primary.DBName}}, DoNothing: true}).Create(values)
	if inserted.Error != nil {
		return dbError(inserted.Error)
	}
	if inserted.RowsAffected == 1 {
		return nil
	}
	guard := map[string]any{primary.DBName: id, "owner_id": ipc.OwnerID(ctx)}
	updated := tx.Model(values).Where(guard).Updates(assignments)
	if updated.Error != nil {
		return dbError(updated.Error)
	}
	if updated.RowsAffected == 1 {
		return nil
	}
	// MySQL/MariaDB report 0 affected rows when the UPDATE sets identical values:
	// distinguish that idempotent no-op from a cross-owner collision.
	var count int64
	if err := tx.Model(values).Where(guard).Count(&count).Error; err != nil {
		return dbError(err)
	}
	if count > 0 {
		return nil
	}
	return ErrWriteConflict
}
