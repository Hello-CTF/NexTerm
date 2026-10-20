package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

// The must-not-exist paths prove ownership from the INSERT result, never from readback
// content equality: a concurrent same-ID insert whose row is byte-identical to this
// call's target (including created_at) must surface ErrWriteConflict, and the concurrent
// row survives. The Expected path rejects the identical row through the revision-bound
// no-op check (the row's revision differs from the expected one) and, when the identical
// row lands right before the INSERT, through the duplicate-key violation itself; the
// ExpectedContent path rejects it through the duplicate-key violation.
func TestUpsertOwnerGuardedExpectedIdenticalInsertDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	columns := []string{"name", "kind", "cipher", "nonce", "blob", "kek_hint", "updated_at"}
	target := store.CredentialRow{
		ID: ids.New(), Name: "imported", Kind: "password", Cipher: store.CipherAES256GCM,
		Nonce: []byte{1, 2, 3}, Blob: []byte{4, 5, 6}, KEKHint: "master:0", CreatedAt: 200, UpdatedAt: 200,
	}

	t.Run("identical row before the probe", func(t *testing.T) {
		row := target
		row.ID = ids.New()
		concurrent := row
		fired := false
		if err := db.GORM().Callback().Update().After("gorm:update").Register("test:identical-insert-expected", func(tx *gorm.DB) {
			if fired || tx.Statement.Table != "credential" {
				return
			}
			fired = true
			if err := db.GORM().WithContext(context.Background()).Create(&concurrent).Error; err != nil {
				t.Errorf("concurrent identical insert: %v", err)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.GORM().Callback().Update().Remove("test:identical-insert-expected") })

		if err := store.UpsertOwnerGuardedExpected(ctx, db.GORM(), &row, columns, 0); !errors.Is(err, store.ErrWriteConflict) {
			t.Fatalf("identical concurrent insert error = %v, want ErrWriteConflict", err)
		}
		stored, err := db.CredentialGetRow(ctx, row.ID)
		if err != nil || stored.Name != "imported" || stored.UpdatedAt != 200 {
			t.Fatalf("concurrent identical row must survive: %+v err=%v", stored, err)
		}
	})

	t.Run("identical row at the insert", func(t *testing.T) {
		row := target
		row.ID = ids.New()
		concurrent := row
		fired := false
		if err := db.GORM().Callback().Query().After("gorm:query").Register("test:identical-insert-expected-count", func(tx *gorm.DB) {
			sql := tx.Statement.SQL.String()
			if fired || tx.Statement.Table != "credential" || !strings.Contains(strings.ToLower(sql), "count(") {
				return
			}
			fired = true
			if err := db.GORM().WithContext(context.Background()).Create(&concurrent).Error; err != nil {
				t.Errorf("concurrent identical insert: %v", err)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.GORM().Callback().Query().Remove("test:identical-insert-expected-count") })

		if err := store.UpsertOwnerGuardedExpected(ctx, db.GORM(), &row, columns, 0); !errors.Is(err, store.ErrWriteConflict) {
			t.Fatalf("identical concurrent insert error = %v, want ErrWriteConflict", err)
		}
		if !fired {
			t.Fatal("identical concurrent insert did not fire")
		}
		stored, err := db.CredentialGetRow(ctx, row.ID)
		if err != nil || stored.Name != "imported" || stored.UpdatedAt != 200 {
			t.Fatalf("concurrent identical row must survive: %+v err=%v", stored, err)
		}
	})
}

// The ExpectedContent must-not-exist path proves ownership from the INSERT result: a
// concurrent same-ID insert whose row is byte-identical to this call's target (including
// created_at) between the absence probe and the INSERT surfaces ErrWriteConflict through
// the duplicate-key violation, and the concurrent row survives.
func TestUpsertOwnerGuardedExpectedContentIdenticalInsertDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	columns := []string{"name", "kind", "cipher", "nonce", "blob", "kek_hint", "updated_at"}
	row := store.CredentialRow{
		ID: ids.New(), Name: "imported", Kind: "password", Cipher: store.CipherAES256GCM,
		Nonce: []byte{1, 2, 3}, Blob: []byte{4, 5, 6}, KEKHint: "master:0", CreatedAt: 200, UpdatedAt: 200,
	}
	snapshot := store.CredentialRow{
		ID: row.ID, Name: "before", Kind: "password", Cipher: store.CipherAES256GCM,
		Nonce: []byte{7}, Blob: []byte{8}, KEKHint: "master:0", CreatedAt: 100, UpdatedAt: 100,
	}
	concurrent := row
	fired := false
	if err := db.GORM().Callback().Query().After("gorm:query").Register("test:identical-insert-content", func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if fired || tx.Statement.Table != "credential" || !strings.Contains(strings.ToLower(sql), "count(") {
			return
		}
		fired = true
		if err := db.GORM().WithContext(context.Background()).Create(&concurrent).Error; err != nil {
			t.Errorf("concurrent identical insert: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.GORM().Callback().Query().Remove("test:identical-insert-content") })

	if err := store.UpsertOwnerGuardedExpectedContent(ctx, db.GORM(), &row, columns, &snapshot); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("identical concurrent insert error = %v, want ErrWriteConflict", err)
	}
	if !fired {
		t.Fatal("identical concurrent insert did not fire")
	}
	stored, err := db.CredentialGetRow(ctx, row.ID)
	if err != nil || stored.Name != "imported" || stored.UpdatedAt != 200 {
		t.Fatalf("concurrent identical row must survive: %+v err=%v", stored, err)
	}
}
