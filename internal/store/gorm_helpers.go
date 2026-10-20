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

// UpsertOwnerGuarded writes values by primary key with an owner guard that behaves
// identically on every supported backend. clause.OnConflict's DO UPDATE ... WHERE guard is
// silently dropped by the MySQL/MariaDB dialect (rendered as ON DUPLICATE KEY UPDATE
// without WHERE), so the portable shape probes existence first, updates an owner-matching
// row in place, and inserts with ON CONFLICT DO NOTHING only when absent — never aborting
// the surrounding transaction on PostgreSQL. Every write path then runs the owner-guarded
// UPDATE so a concurrent same-owner insert cannot be mistaken for this call's success, and
// the assignments are default-aware: zero-value fields carrying a database default are
// omitted (matching GORM Create semantics) so database defaults survive instead of being
// overwritten with zero values. A cross-owner ID collision surfaces as ErrWriteConflict on
// every backend, and re-applying an identical row stays a successful no-op.
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
		value, isZero := field.ValueOf(ctx, elem)
		// GORM Create omits zero-value fields that carry a database default; mirror that
		// so the default survives instead of being overwritten with a zero value.
		if field.HasDefaultValue && isZero {
			continue
		}
		assignments[column] = value
	}
	tx := db.WithContext(ctx)
	guard := map[string]any{primary.DBName: id, "owner_id": ipc.OwnerID(ctx)}
	countOwnerRow := func() (int64, error) {
		var count int64
		if err := tx.Model(values).Where(guard).Count(&count).Error; err != nil {
			return 0, dbError(err)
		}
		return count, nil
	}
	guardedUpdate := func() (bool, error) {
		updated := tx.Model(values).Where(guard).Updates(assignments)
		if updated.Error != nil {
			return false, dbError(updated.Error)
		}
		if updated.RowsAffected == 1 {
			return true, nil
		}
		// MySQL/MariaDB with clientFoundRows=false report 0 affected rows when the UPDATE
		// sets identical values: that is an idempotent no-op, and a foreign or deleted row
		// falls through to the insert or the conflict below.
		existing, err := countOwnerRow()
		if err != nil {
			return false, err
		}
		return existing > 0, nil
	}
	existing, err := countOwnerRow()
	if err != nil {
		return err
	}
	if existing > 0 {
		landed, err := guardedUpdate()
		if err != nil {
			return err
		}
		if landed {
			return nil
		}
	}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: primary.DBName}}, DoNothing: true}).Create(values).Error; err != nil {
		return dbError(err)
	}
	// The guarded UPDATE lands this call's content whether the insert created the row or a
	// concurrent same-owner writer did; with default-aware assignments a fresh row keeps
	// its database defaults and a concurrent row takes this call's content.
	landed, err := guardedUpdate()
	if err != nil {
		return err
	}
	if landed {
		return nil
	}
	return ErrWriteConflict
}
