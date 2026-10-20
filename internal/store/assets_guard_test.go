package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"gorm.io/gorm"
)

// The guarded upsert lands only when the decision snapshot is still current: a concurrent
// local write (user edit, reconciler, or another sync round) between the snapshot read and
// the write surfaces as ErrWriteConflict and must leave the row untouched.
func TestAssetUpsertGuardedConflictMatrix(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	otherCtx := ipc.WithUserID(context.Background(), "other-owner")

	base := AssetRow{ID: ids.New(), Kind: "ssh", Name: "guarded", OptionsJSON: "{}", CreatedAt: 10, UpdatedAt: 100}
	created, err := db.AssetUpsertGuarded(ctx, base, nil)
	if err != nil || !created {
		t.Fatalf("guarded insert created=%v err=%v", created, err)
	}
	// nil base on an existing row: must-not-exist guard fails.
	if _, err := db.AssetUpsertGuarded(ctx, AssetRow{ID: base.ID, Kind: "ssh", Name: "conflict", OptionsJSON: "{}", CreatedAt: 10, UpdatedAt: 200}, nil); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("insert guard error = %v, want ErrWriteConflict", err)
	}
	stored, err := db.AssetGet(ctx, base.ID)
	if err != nil || stored.Name != "guarded" || stored.UpdatedAt != 100 {
		t.Fatalf("row after rejected insert = %+v err=%v", stored, err)
	}

	// A concurrent local write invalidates the snapshot.
	snapshot := stored
	concurrent, err := db.AssetUpdate(ctx, base.ID, AssetPatch{Host: Value("reconciler.example.com")})
	if err != nil {
		t.Fatal(err)
	}
	stale := snapshot
	stale.Host = ptr("sync.example.com")
	stale.UpdatedAt = 200
	if _, err := db.AssetUpsertGuarded(ctx, stale, &snapshot); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("stale snapshot error = %v, want ErrWriteConflict", err)
	}
	after, err := db.AssetGet(ctx, base.ID)
	if err != nil || after.Host == nil || *after.Host != "reconciler.example.com" || after.UpdatedAt != concurrent.UpdatedAt {
		t.Fatalf("concurrent write overwritten: %+v err=%v", after, err)
	}

	// The current snapshot applies cleanly and preserves the peer revision.
	fresh := after
	fresh.Host = ptr("sync.example.com")
	fresh.UpdatedAt = 300
	created, err = db.AssetUpsertGuarded(ctx, fresh, &after)
	if err != nil || created {
		t.Fatalf("fresh snapshot created=%v err=%v", created, err)
	}
	// Re-applying the identical row is an idempotent no-op, not a conflict.
	if _, err := db.AssetUpsertGuarded(ctx, fresh, &fresh); err != nil {
		t.Fatalf("idempotent re-apply err=%v", err)
	}

	// A snapshot taken before a delete cannot resurrect or overwrite the row.
	live, err := db.AssetGet(ctx, base.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AssetDelete(ctx, base.ID); err != nil {
		t.Fatal(err)
	}
	deleted := live
	deleted.Host = ptr("revive.example.com")
	deleted.UpdatedAt = 400
	deleted.DeletedAt = nil
	if _, err := db.AssetUpsertGuarded(ctx, deleted, &live); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("post-delete snapshot error = %v, want ErrWriteConflict", err)
	}
	afterDelete, err := db.AssetGet(ctx, base.ID)
	if err != nil || afterDelete.DeletedAt == nil || afterDelete.Host == nil || *afterDelete.Host != "sync.example.com" {
		t.Fatalf("deleted row mutated: %+v err=%v", afterDelete, err)
	}

	// Cross-owner snapshots never write: the owner guard rejects the row.
	foreign := AssetRow{ID: ids.New(), Kind: "ssh", Name: "foreign", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100}
	if _, err := db.AssetUpsertGuarded(otherCtx, foreign, nil); err != nil {
		t.Fatal(err)
	}
	foreignSnap, err := db.AssetGet(otherCtx, foreign.ID)
	if err != nil {
		t.Fatal(err)
	}
	foreignSnap.Name = "cross-owner"
	foreignSnap.UpdatedAt = 200
	if _, err := db.AssetUpsertGuarded(ctx, foreignSnap, &foreignSnap); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("cross-owner guarded upsert error = %v, want ErrWriteConflict", err)
	}
	untouched, err := db.AssetGet(otherCtx, foreign.ID)
	if err != nil || untouched.Name != "foreign" {
		t.Fatalf("cross-owner row mutated: %+v err=%v", untouched, err)
	}
}

// Every local write path advances the row revision strictly monotonically, so an equal
// updated_at reliably means "unchanged" for CAS consumers even within one millisecond.
func TestAssetWriteRevisionsAreMonotonic(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	asset, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "clock", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	previous := asset.UpdatedAt
	for index := 0; index < 64; index++ {
		updated, err := db.AssetUpdate(ctx, asset.ID, AssetPatch{Note: ptr("edit")})
		if err != nil {
			t.Fatal(err)
		}
		if updated.UpdatedAt <= previous {
			t.Fatalf("revision did not advance: previous=%d current=%d", previous, updated.UpdatedAt)
		}
		previous = updated.UpdatedAt
	}
	// A same-millisecond delete still advances the effective revision past the last update.
	latest, err := db.AssetGet(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AssetDelete(ctx, asset.ID); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.AssetGet(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.DeletedAt == nil || *deleted.DeletedAt <= latest.UpdatedAt {
		t.Fatalf("delete revision must advance past update: updated_at=%d deleted_at=%v", latest.UpdatedAt, deleted.DeletedAt)
	}

	// Same-millisecond ABA: a snapshot taken before a rapid local edit cannot overwrite it.
	second, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "aba", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := second
	for index := 0; index < 8; index++ {
		if _, err := db.AssetUpdate(ctx, second.ID, AssetPatch{Host: Value("local-edit")}); err != nil {
			t.Fatal(err)
		}
	}
	stale := snapshot
	stale.Host = ptr("aba-overwrite")
	stale.UpdatedAt = snapshot.UpdatedAt + 1000
	if _, err := db.AssetUpsertGuarded(ctx, stale, &snapshot); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("same-millisecond ABA error = %v, want ErrWriteConflict", err)
	}
	final, err := db.AssetGet(ctx, second.ID)
	if err != nil || final.Host == nil || *final.Host != "local-edit" {
		t.Fatalf("ABA overwrite landed: %+v err=%v", final, err)
	}
}

// Sync writes share the storage tag conventions: trim, case-insensitive dedup, and
// Chinese/English comma handling, while environment conventions keep working.
func TestAssetUpsertNormalizesTags(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	row := AssetRow{
		ID: ids.New(), Kind: "ssh", Name: "tags", OptionsJSON: "{}",
		Tags: " Prod ， prod， Env:QA ,env:qa ,，staging ", CreatedAt: 1, UpdatedAt: 100,
	}
	if _, err := db.AssetUpsert(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, err := db.AssetGet(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tags != "Prod, Env:QA, staging" {
		t.Fatalf("normalized tags = %q", got.Tags)
	}
	if environment := AssetEnvironment(got); environment != "production" {
		t.Fatalf("environment = %q, want production", environment)
	}
	if values := AssetTagValues(got.Tags); len(values) != 3 || values[0] != "Prod" || values[1] != "Env:QA" || values[2] != "staging" {
		t.Fatalf("tag values = %+v", values)
	}

	guarded := AssetRow{ID: ids.New(), Kind: "ssh", Name: "guarded-tags", OptionsJSON: "{}", Tags: "dev， DEV", CreatedAt: 1, UpdatedAt: 100}
	if _, err := db.AssetUpsertGuarded(ctx, guarded, nil); err != nil {
		t.Fatal(err)
	}
	guardedGot, err := db.AssetGet(ctx, guarded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if guardedGot.Tags != "dev" {
		t.Fatalf("guarded normalized tags = %q", guardedGot.Tags)
	}
	if environment := AssetEnvironment(guardedGot); environment != "development" {
		t.Fatalf("guarded environment = %q, want development", environment)
	}
}

func TestCredentialDeleteIfUnreferenced(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	sharedID, err := db.CredentialPut(ctx, CredentialInput{Name: "shared", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	orphanID, err := db.CredentialPut(ctx, CredentialInput{Name: "orphan", Kind: "password", Nonce: []byte{3}, Blob: []byte{4}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "user", CredID: &sharedID, OptionsJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.CredentialDeleteIfUnreferenced(ctx, sharedID)
	if err != nil || deleted {
		t.Fatalf("referenced credential deleted=%v err=%v", deleted, err)
	}
	deleted, err = db.CredentialDeleteIfUnreferenced(ctx, orphanID)
	if err != nil || !deleted {
		t.Fatalf("orphan credential deleted=%v err=%v", deleted, err)
	}
	if _, err := db.CredentialGetRow(ctx, orphanID); err == nil {
		t.Fatal("orphan credential still present")
	}
	// Soft-deleted assets keep their binding: their credentials are not orphans.
	retiredID, err := db.CredentialPut(ctx, CredentialInput{Name: "retired", Kind: "password", Nonce: []byte{5}, Blob: []byte{6}})
	if err != nil {
		t.Fatal(err)
	}
	retiredAsset, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "retired", CredID: &retiredID, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AssetDelete(ctx, retiredAsset.ID); err != nil {
		t.Fatal(err)
	}
	deleted, err = db.CredentialDeleteIfUnreferenced(ctx, retiredID)
	if err != nil || deleted {
		t.Fatalf("soft-deleted asset credential deleted=%v err=%v", deleted, err)
	}
}

// The owner guard behaves identically on every backend: a cross-owner upsert with the same
// ID is an explicit conflict and never touches the other owner's row (previously the guard
// was silently dropped on MySQL/MariaDB, which rendered ON CONFLICT ... WHERE without it).
func TestUpsertOwnerGuardCrossOwnerConflict(t *testing.T) {
	ctx := context.Background()
	otherCtx := ipc.WithUserID(context.Background(), "other-owner")
	db := testStore(t)

	asset := AssetRow{ID: ids.New(), Kind: "ssh", Name: "owned", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100}
	if _, err := db.AssetUpsert(ctx, asset); err != nil {
		t.Fatal(err)
	}
	asset.Name = "invader"
	asset.UpdatedAt = 200
	if _, err := db.AssetUpsert(otherCtx, asset); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("cross-owner asset upsert error = %v, want ErrWriteConflict", err)
	}
	stored, err := db.AssetGet(ctx, asset.ID)
	if err != nil || stored.Name != "owned" || stored.UpdatedAt != 100 {
		t.Fatalf("cross-owner asset upsert landed: %+v err=%v", stored, err)
	}

	credentialID, err := db.CredentialPut(ctx, CredentialInput{ID: ids.New(), Name: "owned", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CredentialPut(otherCtx, CredentialInput{ID: credentialID, Name: "invader", Kind: "password", Nonce: []byte{3}, Blob: []byte{4}}); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("cross-owner credential upsert error = %v, want ErrWriteConflict", err)
	}
	storedCred, err := db.CredentialGetRow(ctx, credentialID)
	if err != nil || storedCred.Name != "owned" {
		t.Fatalf("cross-owner credential upsert landed: %+v err=%v", storedCred, err)
	}

	// Same-owner re-upsert stays idempotent on all backends.
	asset.UpdatedAt = 300
	if _, err := db.AssetUpsert(ctx, asset); err != nil {
		t.Fatalf("same-owner re-upsert err=%v", err)
	}
	if _, err := db.AssetUpsert(ctx, asset); err != nil {
		t.Fatalf("same-owner idempotent re-upsert err=%v", err)
	}
}

// Same-version ABA: two local edits that interleave on the same base revision must not
// land on the same revision with different content. The base revision is future-dated so
// nextWriteVersion returns expected+1 and the collision is real: with a non-atomic
// read-compute-write both edits land on R+1 and the second silently overwrites the first;
// the expected-revision write forces the second edit to retry onto a fresh revision.
func TestAssetUpdateInterleavedEditsAdvanceRevisionAtomically(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	base := ids.NowMS() + 10_000
	asset := AssetRow{ID: ids.New(), Kind: "ssh", Name: "aba", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: base}
	if _, err := db.AssetUpsert(ctx, asset); err != nil {
		t.Fatal(err)
	}

	// U2's edit lands revision R+1 while U1 is reading R: on the first asset read during
	// U1's update, a direct write advances the row, simulating U2's stale-read write.
	fired := false
	if err := db.GORM().Callback().Query().After("gorm:query").Register("test:aba-interleave", func(tx *gorm.DB) {
		if fired || !strings.Contains(tx.Statement.SQL.String(), "FROM `asset`") {
			return
		}
		fired = true
		if _, err := db.DB().ExecContext(ctx,
			"UPDATE asset SET host = ?, updated_at = ? WHERE id = ?", "edit-2", base+1, asset.ID); err != nil {
			t.Errorf("interleaved edit: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.GORM().Callback().Query().Remove("test:aba-interleave") })

	// U1's first write attempt (computed from R) conflicts with U2's R+1 and retries
	// onto R+2; the edit-2 content is never flattened into the same revision.
	updated, err := db.AssetUpdate(ctx, asset.ID, AssetPatch{Host: Value("edit-1")})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Host == nil || *updated.Host != "edit-1" {
		t.Fatalf("final host = %+v, want edit-1", updated.Host)
	}
	if updated.UpdatedAt != base+2 {
		t.Fatalf("final revision = %d, want %d (atomic retry onto a fresh revision)", updated.UpdatedAt, base+2)
	}
}

// A concurrent same-owner insert landing between the existence probe and the upsert must
// not be mistaken for this call's success: the guarded UPDATE after the insert attempt
// lands the desired content on the concurrent row instead of silently returning it.
func TestAssetUpsertConcurrentSameOwnerInsertLandsDesiredContent(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	assetID := ids.New()

	// On the first existence probe, a concurrent writer inserts the same ID with different
	// content, so the upsert's INSERT is a no-op and only the guarded UPDATE can land.
	fired := false
	if err := db.GORM().Callback().Query().After("gorm:query").Register("test:concurrent-insert", func(tx *gorm.DB) {
		if fired || !strings.Contains(tx.Statement.SQL.String(), "count(*)") || !strings.Contains(tx.Statement.SQL.String(), "FROM `asset`") {
			return
		}
		fired = true
		concurrent := AssetRow{ID: assetID, Kind: "ssh", Name: "concurrent", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100}
		if _, err := db.AssetUpsert(ctx, concurrent); err != nil {
			t.Errorf("concurrent insert: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.GORM().Callback().Query().Remove("test:concurrent-insert") })

	desired := AssetRow{ID: assetID, Kind: "ssh", Name: "desired", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 200}
	if _, err := db.AssetUpsert(ctx, desired); err != nil {
		t.Fatal(err)
	}
	stored, err := db.AssetGet(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "desired" || stored.UpdatedAt != 200 {
		t.Fatalf("concurrent insert misjudged as success: got %+v, want desired/200", stored)
	}
}
