package store_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// Expected-revision upsert contract on real backends: a stale decision snapshot, a
// must-not-exist violation, and a cross-owner snapshot all fail with ErrWriteConflict and
// leave the row untouched; a current snapshot and an identical re-apply succeed.
func TestAssetUpsertGuardedDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "guard-owner")
	otherCtx := ipc.WithUserID(context.Background(), "other-owner")

	base := store.AssetRow{ID: ids.New(), Kind: "ssh", Name: "guarded", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100}
	created, err := db.AssetUpsertGuarded(ctx, base, nil)
	if err != nil || !created {
		t.Fatalf("guarded insert created=%v err=%v", created, err)
	}
	if _, err := db.AssetUpsertGuarded(ctx, store.AssetRow{ID: base.ID, Kind: "ssh", Name: "dup", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 200}, nil); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("insert guard error = %v, want ErrWriteConflict", err)
	}

	snapshot, err := db.AssetGet(ctx, base.ID)
	if err != nil {
		t.Fatal(err)
	}
	concurrent, err := db.AssetUpdate(ctx, base.ID, store.AssetPatch{Host: store.Value("reconciler.example.com")})
	if err != nil {
		t.Fatal(err)
	}
	stale := snapshot
	stale.Host = stringPointer("sync.example.com")
	stale.UpdatedAt = 200
	if _, err := db.AssetUpsertGuarded(ctx, stale, &snapshot); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("stale snapshot error = %v, want ErrWriteConflict", err)
	}
	after, err := db.AssetGet(ctx, base.ID)
	if err != nil || after.Host == nil || *after.Host != "reconciler.example.com" || after.UpdatedAt != concurrent.UpdatedAt {
		t.Fatalf("concurrent write overwritten: %+v err=%v", after, err)
	}

	fresh := after
	fresh.Host = stringPointer("sync.example.com")
	fresh.UpdatedAt = 300
	if created, err := db.AssetUpsertGuarded(ctx, fresh, &after); err != nil || created {
		t.Fatalf("fresh snapshot created=%v err=%v", created, err)
	}
	if _, err := db.AssetUpsertGuarded(ctx, fresh, &fresh); err != nil {
		t.Fatalf("idempotent re-apply err=%v", err)
	}

	foreign := store.AssetRow{ID: ids.New(), Kind: "ssh", Name: "foreign", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100}
	if _, err := db.AssetUpsertGuarded(otherCtx, foreign, nil); err != nil {
		t.Fatal(err)
	}
	foreignSnap, err := db.AssetGet(otherCtx, foreign.ID)
	if err != nil {
		t.Fatal(err)
	}
	foreignSnap.Name = "cross-owner"
	foreignSnap.UpdatedAt = 200
	if _, err := db.AssetUpsertGuarded(ctx, foreignSnap, &foreignSnap); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("cross-owner guarded upsert error = %v, want ErrWriteConflict", err)
	}
	untouched, err := db.AssetGet(otherCtx, foreign.ID)
	if err != nil || untouched.Name != "foreign" {
		t.Fatalf("cross-owner row mutated: %+v err=%v", untouched, err)
	}
}

// The owner guard behaves identically on every backend: a cross-owner upsert with the same
// ID is an explicit conflict and never touches the other owner's row. The MySQL/MariaDB
// dialect silently dropped the ON CONFLICT ... WHERE guard before the portable
// insert-if-absent + guarded-update shape (F-04).
func TestUpsertOwnerGuardCrossOwnerDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "guard-owner")
	otherCtx := ipc.WithUserID(context.Background(), "other-owner")

	asset := store.AssetRow{ID: ids.New(), Kind: "ssh", Name: "owned", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100}
	if _, err := db.AssetUpsert(ctx, asset); err != nil {
		t.Fatal(err)
	}
	asset.Name = "invader"
	asset.UpdatedAt = 200
	if _, err := db.AssetUpsert(otherCtx, asset); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("cross-owner asset upsert error = %v, want ErrWriteConflict", err)
	}
	stored, err := db.AssetGet(ctx, asset.ID)
	if err != nil || stored.Name != "owned" || stored.UpdatedAt != 100 {
		t.Fatalf("cross-owner asset upsert landed: %+v err=%v", stored, err)
	}

	credentialID, err := db.CredentialPut(ctx, store.CredentialInput{ID: ids.New(), Name: "owned", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CredentialPut(otherCtx, store.CredentialInput{ID: credentialID, Name: "invader", Kind: "password", Nonce: []byte{3}, Blob: []byte{4}}); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("cross-owner credential upsert error = %v, want ErrWriteConflict", err)
	}
	storedCred, err := db.CredentialGetRow(ctx, credentialID)
	if err != nil || storedCred.Name != "owned" {
		t.Fatalf("cross-owner credential upsert landed: %+v err=%v", storedCred, err)
	}

	// Same-owner re-upsert stays idempotent on all backends, and a changed upsert
	// actually lands in the row (the guard must not silently skip the UPDATE).
	asset.UpdatedAt = 300
	if _, err := db.AssetUpsert(ctx, asset); err != nil {
		t.Fatalf("same-owner changed upsert err=%v", err)
	}
	landed, err := db.AssetGet(ctx, asset.ID)
	if err != nil || landed.Name != "invader" || landed.UpdatedAt != 300 {
		t.Fatalf("same-owner changed upsert did not land: %+v err=%v", landed, err)
	}
	if _, err := db.AssetUpsert(ctx, asset); err != nil {
		t.Fatalf("same-owner idempotent re-upsert err=%v", err)
	}
	landed, err = db.AssetGet(ctx, asset.ID)
	if err != nil || landed.Name != "invader" || landed.UpdatedAt != 300 {
		t.Fatalf("idempotent re-upsert changed the row: %+v err=%v", landed, err)
	}
	// Same-owner changed credential upsert lands as well.
	if _, err := db.CredentialPut(ctx, store.CredentialInput{ID: credentialID, Name: "renamed", Kind: "password", Nonce: []byte{5}, Blob: []byte{6}}); err != nil {
		t.Fatalf("same-owner changed credential upsert err=%v", err)
	}
	landedCred, err := db.CredentialGetRow(ctx, credentialID)
	if err != nil || landedCred.Name != "renamed" || !bytes.Equal(landedCred.Blob, []byte{6}) {
		t.Fatalf("same-owner changed credential upsert did not land: %+v err=%v", landedCred, err)
	}
}

// Local writes advance the revision strictly monotonically on every backend: a rapid
// same-millisecond edit cannot leave the revision unchanged, so a snapshot taken before it
// is reliably detected as stale (no ABA window for the guarded upsert).
func TestAssetRevisionMonotonicDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "guard-owner")

	asset, err := db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "clock", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	previous := asset.UpdatedAt
	for index := 0; index < 64; index++ {
		updated, err := db.AssetUpdate(ctx, asset.ID, store.AssetPatch{Note: stringPointer("edit")})
		if err != nil {
			t.Fatal(err)
		}
		if updated.UpdatedAt <= previous {
			t.Fatalf("revision did not advance: previous=%d current=%d", previous, updated.UpdatedAt)
		}
		previous = updated.UpdatedAt
	}

	second, err := db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "aba", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := second
	for index := 0; index < 8; index++ {
		if _, err := db.AssetUpdate(ctx, second.ID, store.AssetPatch{Host: store.Value("local-edit")}); err != nil {
			t.Fatal(err)
		}
	}
	stale := snapshot
	stale.Host = stringPointer("aba-overwrite")
	stale.UpdatedAt = snapshot.UpdatedAt + 1000
	if _, err := db.AssetUpsertGuarded(ctx, stale, &snapshot); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("same-millisecond ABA error = %v, want ErrWriteConflict", err)
	}
	final, err := db.AssetGet(ctx, second.ID)
	if err != nil || final.Host == nil || *final.Host != "local-edit" {
		t.Fatalf("ABA overwrite landed: %+v err=%v", final, err)
	}

	latest, err := db.AssetGet(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AssetDelete(ctx, asset.ID); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.AssetGet(ctx, asset.ID)
	if err != nil || deleted.DeletedAt == nil || *deleted.DeletedAt <= latest.UpdatedAt {
		t.Fatalf("delete revision must advance past update: %+v err=%v", deleted, err)
	}
}
