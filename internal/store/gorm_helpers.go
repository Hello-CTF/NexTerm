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
	return UpsertOwnerGuardedLWW(ctx, db, values, columns, "updated_at")
}

// UpsertOwnerGuarded writes values by primary key with an owner guard and a
// revision-ordering guard that behave identically on every supported backend.
// clause.OnConflict's DO UPDATE ... WHERE guard is silently dropped by the MySQL/MariaDB
// dialect (rendered as ON DUPLICATE KEY UPDATE without WHERE), so the portable shape
// probes existence first, updates an owner-matching row in place, and inserts with
// ON CONFLICT DO NOTHING only when absent — never aborting the surrounding transaction on
// PostgreSQL. The UPDATE is guarded by revisionColumn < the value's own revision: a
// concurrent or later write with a newer revision is never overwritten (it wins), and an
// equal-revision row with different content surfaces ErrWriteConflict — the caller
// decides equal-revision winners (the sync planner's LWW) and lands them through the
// snapshot-bound CAS variants. A cross-owner ID collision surfaces as ErrWriteConflict on
// every backend, and re-applying an identical row stays a successful no-op via a content
// comparison fallback.
func UpsertOwnerGuarded(ctx context.Context, db *gorm.DB, values any, columns []string, revisionColumn string) error {
	return upsertOwnerGuarded(ctx, db, values, columns, revisionColumn, true)
}

// UpsertOwnerGuardedLWW writes values by primary key with an owner guard for local
// last-write-wins upserts: an existing owner-matching row takes this call's content
// regardless of revision (the caller decides ordering; local writes carry monotonic
// revisions). The follow-up UPDATE after the insert attempt keeps the revision-ordering
// guard, so a concurrent write with a newer or equal revision that won the insert race is
// never overwritten. A cross-owner ID collision surfaces as ErrWriteConflict on every
// backend, and re-applying an identical row stays a successful no-op via a content
// comparison fallback.
func UpsertOwnerGuardedLWW(ctx context.Context, db *gorm.DB, values any, columns []string, revisionColumn string) error {
	return upsertOwnerGuarded(ctx, db, values, columns, revisionColumn, false)
}

func upsertOwnerGuarded(ctx context.Context, db *gorm.DB, values any, columns []string, revisionColumn string, guardExistingRow bool) error {
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
	revisionField := statement.Schema.LookUpField(revisionColumn)
	if revisionField == nil {
		return dbError(fmt.Errorf("owner-guarded upsert revision column %s not found in schema", revisionColumn))
	}
	myRevision, _ := revisionField.ValueOf(ctx, elem)
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
		statement := tx.Model(values).Where(guard).Where(revisionColumn+" < ?", myRevision)
		updated := statement.Updates(assignments)
		if updated.Error != nil {
			return false, dbError(updated.Error)
		}
		if updated.RowsAffected == 1 {
			return true, nil
		}
		// The guarded UPDATE landed nothing: the row is either identical (MySQL/MariaDB
		// with clientFoundRows=false report 0 for a no-op) or carries content this call
		// must not overwrite (a concurrent or later write wins).
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
		if guardExistingRow {
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
		// Local last-write-wins: the existing owner-matching row takes this call's content.
		updated := tx.Model(values).Where(guard).Updates(assignments)
		if updated.Error != nil {
			return dbError(updated.Error)
		}
		if updated.RowsAffected == 1 {
			return nil
		}
		// MySQL/MariaDB with clientFoundRows=false report 0 for a no-op UPDATE: accept the
		// write only when the stored row already equals the desired assignments.
		matches, err := ownerRowMatchesAssignments(ctx, tx, values, guard, assignments)
		if err != nil {
			return err
		}
		if matches {
			return nil
		}
		return ErrWriteConflict
	}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: primary.DBName}}, DoNothing: true}).Create(values).Error; err != nil {
		return dbError(err)
	}
	// The guarded UPDATE lands this call's content only when the row still carries what
	// this call inserted; a concurrent or later write with a different or newer revision
	// is never overwritten (the later write wins).
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

// ownerRowMatchesContent reports whether the stored row carries exactly the given row's
// full content (every column except the primary key and owner_id); a missing row reports
// false.
func ownerRowMatchesContent(ctx context.Context, tx *gorm.DB, values any, guard map[string]any, statement *gorm.Statement) (bool, error) {
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
	for _, f := range statement.Schema.Fields {
		if f.PrimaryKey || f.DBName == "" || f.DBName == "owner_id" {
			continue
		}
		want, _ := f.ValueOf(ctx, elem)
		got, _ := f.ValueOf(ctx, rowElem)
		if !reflect.DeepEqual(got, want) {
			return false, nil
		}
	}
	return true, nil
}

// UpsertOwnerGuardedExpected writes values by primary key with an owner guard and an
// expected-revision CAS bound to the caller's snapshot: an existing row is updated only
// when its revision still equals expectedRevision, and a new row is inserted only when it
// is still absent. A row that changed since the snapshot (a later local write) is never
// overwritten — the write surfaces ErrWriteConflict instead.
func UpsertOwnerGuardedExpected(ctx context.Context, db *gorm.DB, values any, columns []string, expectedRevision int64) error {
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
	guard := map[string]any{primary.DBName: id, "owner_id": ipc.OwnerID(ctx)}
	updated := tx.Model(values).Where(guard).Where("updated_at = ?", expectedRevision).Updates(assignments)
	if updated.Error != nil {
		return dbError(updated.Error)
	}
	if updated.RowsAffected == 1 {
		return nil
	}
	// The guarded UPDATE landed nothing: either the row changed since the snapshot (a
	// later local write wins and is never overwritten) or it is absent. Compare content to
	// tell an identical no-op apart from a genuine conflict.
	matches, err := ownerRowMatchesAssignments(ctx, tx, values, guard, assignments)
	if err != nil {
		return err
	}
	if matches {
		return nil
	}
	var count int64
	if err := tx.Model(values).Where(guard).Count(&count).Error; err != nil {
		return dbError(err)
	}
	if count > 0 {
		return ErrWriteConflict
	}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: primary.DBName}}, DoNothing: true}).Create(values).Error; err != nil {
		return dbError(err)
	}
	// The INSERT is a no-op when a concurrent writer created the same ID first, so the
	// insert alone cannot prove this call landed: accept only when the stored row carries
	// exactly this call's content — a concurrent same-ID insert with different content
	// surfaces ErrWriteConflict instead of being mistaken for this call's success.
	matches, err = ownerRowMatchesContent(ctx, tx, values, guard, statement)
	if err != nil {
		return err
	}
	if matches {
		return nil
	}
	return ErrWriteConflict
}

// UpsertOwnerGuardedExpectedContent writes values by primary key with an owner guard and
// a content CAS bound to the caller's snapshot: an existing row is updated only when its
// full content still equals the snapshot's, and a new row is inserted only when it is
// still absent. A row whose content changed since the snapshot (a later local write) is
// never overwritten — the write surfaces ErrWriteConflict instead. The snapshot must be
// the same row type as values.
func UpsertOwnerGuardedExpectedContent(ctx context.Context, db *gorm.DB, values any, columns []string, snapshot any) error {
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(values); err != nil {
		return dbError(err)
	}
	if statement.Schema == nil || len(statement.Schema.PrimaryFields) != 1 {
		return dbError(fmt.Errorf("owner-guarded upsert requires exactly one primary key field"))
	}
	if reflect.TypeOf(snapshot) != reflect.TypeOf(values) {
		return dbError(fmt.Errorf("owner-guarded content upsert requires the snapshot to match the written row type"))
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
	contentConds, err := snapshotContentConditions(ctx, statement, snapshot)
	if err != nil {
		return err
	}
	tx := db.WithContext(ctx)
	guard := map[string]any{primary.DBName: id, "owner_id": ipc.OwnerID(ctx)}
	// The content CAS binds through dialect-quoted column references built from the
	// schema: a hand-written WHERE string breaks on MySQL/MariaDB, where `blob` is a
	// reserved word (Error 1064).
	updated := tx.Model(values).Where(guard).Where(contentConds).Updates(assignments)
	if updated.Error != nil {
		return dbError(updated.Error)
	}
	if updated.RowsAffected == 1 {
		return nil
	}
	// The guarded UPDATE landed nothing: either the row changed since the snapshot (a
	// later local write wins and is never overwritten) or it is absent. Compare content to
	// tell an identical no-op apart from a genuine conflict.
	matches, err := ownerRowMatchesAssignments(ctx, tx, values, guard, assignments)
	if err != nil {
		return err
	}
	if matches {
		return nil
	}
	var count int64
	if err := tx.Model(values).Where(guard).Count(&count).Error; err != nil {
		return dbError(err)
	}
	if count > 0 {
		return ErrWriteConflict
	}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: primary.DBName}}, DoNothing: true}).Create(values).Error; err != nil {
		return dbError(err)
	}
	// The INSERT is a no-op when a concurrent writer created the same ID first, so the
	// insert alone cannot prove this call landed: accept only when the stored row carries
	// exactly this call's content — a concurrent same-ID insert with different content
	// surfaces ErrWriteConflict instead of being mistaken for this call's success.
	matches, err = ownerRowMatchesContent(ctx, tx, values, guard, statement)
	if err != nil {
		return err
	}
	if matches {
		return nil
	}
	return ErrWriteConflict
}

// snapshotContentConditions builds dialect-quoted equality conditions binding every
// column except the primary key and owner_id to the snapshot's values; nil pointer
// columns bind as IS NULL. The snapshot must be the same row type as the written values.
func snapshotContentConditions(ctx context.Context, statement *gorm.Statement, snapshot any) ([]clause.Expression, error) {
	elem := reflect.ValueOf(snapshot)
	for elem.Kind() == reflect.Pointer {
		elem = elem.Elem()
	}
	conds := make([]clause.Expression, 0, len(statement.Schema.Fields))
	for _, f := range statement.Schema.Fields {
		if f.PrimaryKey || f.DBName == "" || f.DBName == "owner_id" {
			continue
		}
		value, _ := f.ValueOf(ctx, elem)
		if rv := reflect.ValueOf(value); rv.IsValid() && rv.Kind() == reflect.Ptr {
			if rv.IsNil() {
				value = nil
			} else {
				value = rv.Elem().Interface()
			}
		}
		conds = append(conds, clause.Eq{Column: clause.Column{Name: f.DBName}, Value: value})
	}
	return conds, nil
}
