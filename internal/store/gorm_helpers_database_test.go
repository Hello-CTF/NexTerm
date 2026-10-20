package store_test

import (
	"context"
	"errors"
	"os"
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
// created_at) must surface ErrWriteConflict, and the row survives.
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

// The ExpectedContent path never claims a row it did not write: a pre-existing row
// byte-identical to this call's target (including created_at) makes the snapshot CAS
// land nothing, and the write surfaces ErrWriteConflict instead of succeeding on
// readback equality; the identical row survives.
func TestUpsertOwnerGuardedExpectedContentIdenticalInsertDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	row := upsertGuardCredential(ids.New(), "imported", 200, 200)
	if err := db.GORM().WithContext(ctx).Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := upsertGuardCredential(row.ID, "before", 100, 100)
	if _, err := store.UpsertOwnerGuardedExpectedContent(ctx, db.GORM(), &row, upsertGuardCredentialColumns, &snapshot); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("identical pre-existing row error = %v, want ErrWriteConflict", err)
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
// identical write at the expected revision as a no-op; the ExpectedContent path lands a
// snapshot-bound update, replays a refreshed snapshot that already carries the target
// content, conflicts on a same-revision different-content row, and conflicts on a stale
// snapshot whose row merely reads back equal to the target (content equality cannot
// prove ownership).
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

	t.Run("expected content ABA conflict", func(t *testing.T) {
		db := dbtest.NewFixture(t).OpenStore(t)
		ctx := context.Background()
		id := ids.New()
		snapshot := upsertGuardCredential(id, "before", 100, 100)
		current := upsertGuardCredential(id, "later", 100, 100)
		if err := db.GORM().Create(&current).Error; err != nil {
			t.Fatal(err)
		}
		target := upsertGuardCredential(id, "imported", 100, 100)
		if _, err := store.UpsertOwnerGuardedExpectedContent(ctx, db.GORM(), &target, upsertGuardCredentialColumns, &snapshot); !errors.Is(err, store.ErrWriteConflict) {
			t.Fatalf("same-revision ABA error = %v, want ErrWriteConflict", err)
		}
		stored, err := db.CredentialGetRow(ctx, id)
		if err != nil || stored.Name != "later" {
			t.Fatalf("same-revision later write changed: %+v err=%v", stored, err)
		}
	})

	t.Run("expected content replay and stale snapshot", func(t *testing.T) {
		db := dbtest.NewFixture(t).OpenStore(t)
		ctx := context.Background()
		id := ids.New()
		snapshot := upsertGuardCredential(id, "before", 100, 100)
		if err := db.GORM().Create(&snapshot).Error; err != nil {
			t.Fatal(err)
		}
		target := upsertGuardCredential(id, "imported", 100, 100)
		if _, err := store.UpsertOwnerGuardedExpectedContent(ctx, db.GORM(), &target, upsertGuardCredentialColumns, &snapshot); err != nil {
			t.Fatal(err)
		}
		// A genuine idempotent replay — the snapshot refreshed to the target content —
		// succeeds without writing anything.
		replay := target
		if _, err := store.UpsertOwnerGuardedExpectedContent(ctx, db.GORM(), &target, upsertGuardCredentialColumns, &replay); err != nil {
			t.Fatalf("genuine idempotent replay failed: %v", err)
		}
		// A replayed call with the stale snapshot no longer proves ownership: the row
		// reached the target through the earlier write, so it conflicts and survives.
		if _, err := store.UpsertOwnerGuardedExpectedContent(ctx, db.GORM(), &target, upsertGuardCredentialColumns, &snapshot); !errors.Is(err, store.ErrWriteConflict) {
			t.Fatalf("stale snapshot replay error = %v, want ErrWriteConflict", err)
		}
		stored, err := db.CredentialGetRow(ctx, id)
		if err != nil || stored.Name != "imported" {
			t.Fatalf("stored row changed: %+v err=%v", stored, err)
		}
	})
}

// The must-not-exist path also rejects an identical row that lands right before the
// INSERT: the concurrent insert commits while this call sits between its savepoint and
// the INSERT (constructible on PostgreSQL, MySQL, and MariaDB; SQLite's immediate
// transaction write lock blocks the concurrent connection, so the window is not
// constructible there and the test skips — a non-constructible note, not a product pass
// or failure). The duplicate-key violation surfaces ErrWriteConflict and the concurrent
// row survives.
//
//dbtest:backend postgres,mysql,mariadb
func TestUpsertOwnerGuardedExpectedIdenticalInsertAtInsertDatabase(t *testing.T) {
	switch strings.ToLower(os.Getenv("NEXTERM_TEST_DB_BACKEND")) {
	case "postgres", "mysql", "mariadb":
	default:
		t.Skip("the at-insert identical-row window is not constructible on SQLite")
	}
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	row := upsertGuardCredential(ids.New(), "imported", 200, 200)
	concurrent := row
	fired := false
	if err := db.GORM().Callback().Create().Before("gorm:create").Register("test:identical-insert-expected", func(tx *gorm.DB) {
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
	t.Cleanup(func() { _ = db.GORM().Callback().Create().Remove("test:identical-insert-expected") })

	if err := store.UpsertOwnerGuardedExpected(ctx, db.GORM(), &row, upsertGuardCredentialColumns, 0); !errors.Is(err, store.ErrWriteConflict) {
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
