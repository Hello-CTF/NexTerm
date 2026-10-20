package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

var upsertGuardCredentialColumns = []string{"name", "kind", "cipher", "nonce", "blob", "kek_hint", "updated_at"}

func upsertGuardCredential(id, name string, createdAt, updatedAt int64) store.CredentialRow {
	return store.CredentialRow{
		ID: id, Name: name, Kind: "password", Cipher: store.CipherAES256GCM,
		Nonce: []byte{1, 2, 3}, Blob: []byte{4, 5, 6}, KEKHint: "master:0",
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

// The must-not-exist paths prove ownership from the INSERT result, never from readback
// content equality: a pre-existing row byte-identical to this call's target (including
// created_at) must surface ErrWriteConflict, and the row survives. (The in-transaction
// insert window is covered by the bundle-level PostgreSQL tests; on SQLite the store's
// immediate transaction lock makes it non-constructible.)
func TestUpsertOwnerGuardedExpectedIdenticalInsertDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()

	row := upsertGuardCredential(ids.New(), "imported", 200, 200)
	if err := db.GORM().WithContext(ctx).Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertOwnerGuardedExpected(ctx, db.GORM(), &row, upsertGuardCredentialColumns, 0); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("identical pre-existing row error = %v, want ErrWriteConflict", err)
	}
	stored, err := db.CredentialGetRow(ctx, row.ID)
	if err != nil || stored.Name != "imported" || stored.UpdatedAt != 200 {
		t.Fatalf("concurrent identical row must survive: %+v err=%v", stored, err)
	}
}

// The ExpectedContent must-not-exist path proves ownership from the INSERT result: a
// concurrent same-ID insert whose row is byte-identical to this call's target (including
// created_at) between the absence probe and the INSERT surfaces ErrWriteConflict through
// the duplicate-key violation, and the concurrent row survives.
func TestUpsertOwnerGuardedExpectedContentIdenticalInsertDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	row := upsertGuardCredential(ids.New(), "imported", 200, 200)
	snapshot := upsertGuardCredential(row.ID, "before", 100, 100)
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

	if err := store.UpsertOwnerGuardedExpectedContent(ctx, db.GORM(), &row, upsertGuardCredentialColumns, &snapshot); !errors.Is(err, store.ErrWriteConflict) {
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

// A pre-existing zero-revision row is never overwritten or mistaken for this call's own
// write by the must-not-exist path: the write goes straight to the ownership-proving
// INSERT, and any existing row — different content, only created_at different, or
// cross-owner — surfaces ErrWriteConflict and survives untouched.
func TestUpsertOwnerGuardedExpectedZeroRevisionMustNotExistDatabase(t *testing.T) {
	for _, test := range []struct {
		name           string
		existingName   string
		targetName     string
		existingCreate int64
		targetCreate   int64
	}{
		{name: "different content", existingName: "concurrent", targetName: "imported", existingCreate: 100, targetCreate: 200},
		{name: "created_at only", existingName: "same", targetName: "same", existingCreate: 100, targetCreate: 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := dbtest.NewFixture(t).OpenStore(t)
			ctx := context.Background()
			id := ids.New()
			existing := upsertGuardCredential(id, test.existingName, test.existingCreate, 0)
			if err := db.GORM().Create(&existing).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.GORM().Model(&store.CredentialRow{}).Where("id = ?", id).UpdateColumn("updated_at", 0).Error; err != nil {
				t.Fatal(err)
			}
			target := upsertGuardCredential(id, test.targetName, test.targetCreate, 0)
			if err := store.UpsertOwnerGuardedExpected(ctx, db.GORM(), &target, upsertGuardCredentialColumns, 0); !errors.Is(err, store.ErrWriteConflict) {
				t.Fatalf("must-not-exist upsert error = %v, want ErrWriteConflict", err)
			}
			stored, err := db.CredentialGetRow(ctx, id)
			if err != nil || stored.Name != test.existingName || stored.CreatedAt != test.existingCreate || stored.UpdatedAt != 0 {
				t.Fatalf("pre-existing row changed: %+v err=%v", stored, err)
			}
		})
	}

	t.Run("cross-owner", func(t *testing.T) {
		db := dbtest.NewFixture(t).OpenStore(t)
		ctxA := ipc.WithUserID(context.Background(), "user-a")
		ctxB := ipc.WithUserID(context.Background(), "user-b")
		id := ids.New()
		existing := upsertGuardCredential(id, "owned", 100, 0)
		existing.OwnerID = "user-a"
		if err := db.GORM().WithContext(ctxA).Create(&existing).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.GORM().WithContext(ctxA).Model(&store.CredentialRow{}).Where("id = ?", id).UpdateColumn("updated_at", 0).Error; err != nil {
			t.Fatal(err)
		}
		target := upsertGuardCredential(id, "invader", 200, 0)
		target.OwnerID = "user-b"
		if err := store.UpsertOwnerGuardedExpected(ctxB, db.GORM(), &target, upsertGuardCredentialColumns, 0); !errors.Is(err, store.ErrWriteConflict) {
			t.Fatalf("cross-owner upsert error = %v, want ErrWriteConflict", err)
		}
		stored, err := db.CredentialGetRow(ctxA, id)
		if err != nil || stored.Name != "owned" || stored.CreatedAt != 100 {
			t.Fatalf("cross-owner row changed: %+v err=%v", stored, err)
		}
	})
}

// The snapshot-bound paths keep their ordinary semantics: the Expected path replays an
// identical write at the expected revision as a no-op, and the ExpectedContent path
// lands a snapshot-bound update and replays it, while a same-revision different-content
// row still conflicts.
func TestUpsertOwnerGuardedSnapshotPathsUnaffectedDatabase(t *testing.T) {
	t.Run("expected idempotent replay", func(t *testing.T) {
		db := dbtest.NewFixture(t).OpenStore(t)
		ctx := context.Background()
		row := upsertGuardCredential(ids.New(), "same", 100, 100)
		if err := store.UpsertOwnerGuardedExpected(ctx, db.GORM(), &row, upsertGuardCredentialColumns, 0); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertOwnerGuardedExpected(ctx, db.GORM(), &row, upsertGuardCredentialColumns, 100); err != nil {
			t.Fatalf("ordinary idempotent replay failed: %v", err)
		}
	})

	t.Run("expected content ABA and replay", func(t *testing.T) {
		db := dbtest.NewFixture(t).OpenStore(t)
		ctx := context.Background()
		id := ids.New()
		snapshot := upsertGuardCredential(id, "before", 100, 100)
		current := upsertGuardCredential(id, "later", 100, 100)
		if err := db.GORM().Create(&current).Error; err != nil {
			t.Fatal(err)
		}
		target := upsertGuardCredential(id, "imported", 100, 100)
		if err := store.UpsertOwnerGuardedExpectedContent(ctx, db.GORM(), &target, upsertGuardCredentialColumns, &snapshot); !errors.Is(err, store.ErrWriteConflict) {
			t.Fatalf("same-revision ABA error = %v, want ErrWriteConflict", err)
		}
		stored, err := db.CredentialGetRow(ctx, id)
		if err != nil || stored.Name != "later" {
			t.Fatalf("same-revision later write changed: %+v err=%v", stored, err)
		}
	})

	t.Run("expected content idempotent replay", func(t *testing.T) {
		db := dbtest.NewFixture(t).OpenStore(t)
		ctx := context.Background()
		id := ids.New()
		snapshot := upsertGuardCredential(id, "before", 100, 100)
		if err := db.GORM().Create(&snapshot).Error; err != nil {
			t.Fatal(err)
		}
		target := upsertGuardCredential(id, "imported", 100, 100)
		if err := store.UpsertOwnerGuardedExpectedContent(ctx, db.GORM(), &target, upsertGuardCredentialColumns, &snapshot); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertOwnerGuardedExpectedContent(ctx, db.GORM(), &target, upsertGuardCredentialColumns, &snapshot); err != nil {
			t.Fatalf("ordinary content idempotent replay failed: %v", err)
		}
	})
}
