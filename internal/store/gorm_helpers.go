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
	return UpsertOwnerGuarded(ctx, db, values, columns, "updated_at")
}

// UpsertOwnerGuarded writes values by primary key with an owner guard and a
// revision-ordering guard that behave identically on every supported backend.
// clause.OnConflict's DO UPDATE ... WHERE guard is silently dropped by the MySQL/MariaDB
// dialect (rendered as ON DUPLICATE KEY UPDATE without WHERE), so the portable shape
// probes existence first, updates an owner-matching row in place, and inserts with
// ON CONFLICT DO NOTHING only when absent — never aborting the surrounding transaction on
// PostgreSQL. The UPDATE is guarded by revisionColumn <= the value's own revision: a
// concurrent or later write with a newer revision is never overwritten (it wins), while
// an older or equal-revision row takes this call's content. A cross-owner ID collision
// surfaces as ErrWriteConflict on every backend, and re-applying an identical row stays a
// successful no-op via a content comparison fallback.
func UpsertOwnerGuarded(ctx context.Context, db *gorm.DB, values any, columns []string, revisionColumn string) error {
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
	var myRevision any
	if revisionColumn != "" {
		revisionField := statement.Schema.LookUpField(revisionColumn)
		if revisionField == nil {
			return dbError(fmt.Errorf("owner-guarded upsert revision column %s not found in schema", revisionColumn))
		}
		myRevision, _ = revisionField.ValueOf(ctx, elem)
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
		statement := tx.Model(values).Where(guard)
		if revisionColumn != "" {
			statement = statement.Where(revisionColumn+" <= ?", myRevision)
		}
		updated := statement.Updates(assignments)
		if updated.Error != nil {
			return false, dbError(updated.Error)
		}
		if updated.RowsAffected == 1 {
			return true, nil
		}
		// The guarded UPDATE landed nothing: either the row is identical (MySQL/MariaDB
		// with clientFoundRows=false report 0 for a no-op), or it carries a newer revision
		// and must not be overwritten. Compare content to tell the two apart.
		matches, err := ownerRowMatchesAssignments(ctx, tx, values, guard, assignments)
		if err != nil {
			return false, err
		}
		return matches, nil
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
		// An existing row whose revision is newer than this call's takes precedence: the
		// later write wins and this upsert reports the conflict instead of overwriting it.
		return ErrWriteConflict
	}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: primary.DBName}}, DoNothing: true}).Create(values).Error; err != nil {
		return dbError(err)
	}
	// The guarded UPDATE lands this call's content whether the insert created the row or a
	// concurrent same-owner writer did; a concurrent or later row with a newer revision is
	// never overwritten.
	landed, err := guardedUpdate()
	if err != nil {
		return err
	}
	if landed {
		return nil
	}
	return ErrWriteConflict
}

// ownerRowMatchesAssignments reports whether the stored row carries exactly the given
// assignment values; a missing row reports false.
func ownerRowMatchesAssignments(ctx context.Context, tx *gorm.DB, values any, guard map[string]any, assignments map[string]any) (bool, error) {
	statement := &gorm.Statement{DB: tx}
	if err := statement.Parse(values); err != nil {
		return false, dbError(err)
	}
	elem := reflect.ValueOf(values)
	for elem.Kind() == reflect.Pointer {
		elem = elem.Elem()
	}
	row := reflect.New(elem.Type()).Interface()
	if err := tx.Where(guard).First(row).Error; err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, dbError(err)
	}
	rowElem := reflect.ValueOf(row).Elem()
	for column, want := range assignments {
		field := statement.Schema.LookUpField(column)
		if field == nil {
			continue
		}
		got, _ := field.ValueOf(ctx, rowElem)
		if !reflect.DeepEqual(got, want) {
			return false, nil
		}
	}
	return true, nil
}
