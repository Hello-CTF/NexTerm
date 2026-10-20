package store

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"gorm.io/gorm"
)

// A later write at the SAME revision must never be overwritten by this call's follow-up
// UPDATE: equal-revision different content is re-decided by content, not by <=revision.
func TestAssetUpsertDoesNotOverwriteSameRevisionLaterWrite(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	assetID := ids.New()

	// After the insert commits, a later write lands at the SAME revision (200) with
	// different content; the follow-up UPDATE must not overwrite it with <=revision.
	fired := false
	if err := db.GORM().Callback().Create().After("gorm:commit_or_rollback_transaction").Register("test:same-rev-later", func(tx *gorm.DB) {
		if fired {
			return
		}
		fired = true
		if _, err := db.DB().ExecContext(ctx,
			"UPDATE asset SET name = ?, updated_at = ? WHERE id = ?", "later-same-revision", 200, assetID); err != nil {
			t.Errorf("later write: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.GORM().Callback().Create().Remove("test:same-rev-later") })

	original := AssetRow{ID: assetID, Kind: "ssh", Name: "imported", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 200}
	if _, err := db.AssetUpsert(ctx, original); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("original call err = %v, want ErrWriteConflict (the later write wins)", err)
	}
	stored, err := db.AssetGet(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "later-same-revision" {
		t.Fatalf("same-revision later write overwritten: got %+v, want later-same-revision", stored)
	}
}

// An asset guarded upsert must detect same-version ABA: the base snapshot (revision R,
// host A) no longer matches the row (revision R, host B), so the write conflicts instead
// of overwriting the interleaved content at the same revision.
func TestAssetUpsertGuardedDetectsSameVersionABA(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	asset := AssetRow{ID: ids.New(), Kind: "ssh", Name: "web", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100}
	if _, err := db.AssetUpsert(ctx, asset); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.AssetGet(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}

	// An interleaved write lands at the SAME revision with different content.
	if _, err := db.DB().ExecContext(ctx,
		"UPDATE asset SET host = ? WHERE id = ?", "interleaved", asset.ID); err != nil {
		t.Fatal(err)
	}

	// The guarded upsert with the stale snapshot (host A) must conflict, not overwrite.
	desired := snapshot
	desired.Host = ptr("imported")
	desired.UpdatedAt = 200
	if _, err := db.AssetUpsertGuarded(ctx, desired, &snapshot); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("same-version ABA error = %v, want ErrWriteConflict", err)
	}
	stored, err := db.AssetGet(ctx, asset.ID)
	if err != nil || stored.Host == nil || *stored.Host != "interleaved" {
		t.Fatalf("interleaved content overwritten: %+v err=%v", stored, err)
	}
}
