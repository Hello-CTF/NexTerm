package store

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestAssetUpdateChangesKind(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	host := "10.0.0.5"
	asset, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "win", Host: &host, Port: ptr(int32(22)), OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := db.AssetUpdate(ctx, asset.ID, AssetPatch{Kind: ptr("rdp"), Port: Value(int32(3389)), OptionsJSON: ptr(`{"domain":"CORP"}`)})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.AssetGet(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Kind != "rdp" || stored.Kind != "rdp" || stored.Port == nil || *stored.Port != 3389 {
		t.Fatalf("kind change not stored: updated=%+v stored=%+v", updated, stored)
	}
	if stored.UpdatedAt == asset.UpdatedAt {
		t.Fatal("a kind-only change must advance the revision")
	}

	_, err = db.AssetUpdate(ctx, asset.ID, AssetPatch{Kind: ptr("")})
	requireCode(t, err, ipc.CodeBadParam)
	_, err = db.AssetUpdate(ctx, asset.ID, AssetPatch{Kind: ptr(AssetKindDevice)})
	requireCode(t, err, ipc.CodeBadParam)
}

func TestAssetUpdateKeepsProtectedKinds(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	if _, err := db.AssetEnsureBuiltinLocal(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := db.AssetUpdate(ctx, BuiltinLocalAssetID, AssetPatch{Kind: ptr("ssh")})
	requireCode(t, err, ipc.CodeBadParam)
	// Saving the built-in asset with its own kind is an ordinary edit.
	if _, err := db.AssetUpdate(ctx, BuiltinLocalAssetID, AssetPatch{Kind: ptr("local"), Note: ptr("note")}); err != nil {
		t.Fatal(err)
	}

	owner := ipc.WithUserID(ctx, "user-a")
	host := "example.com"
	remote, err := db.AssetCreate(owner, AssetInput{Kind: "rdp", Name: "desk", Host: &host, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.AssetUpdate(owner, remote.ID, AssetPatch{Kind: ptr("local")})
	requireCode(t, err, ipc.CodeUnsupported)
	if row, err := db.AssetGet(owner, remote.ID); err != nil || row.Kind != "rdp" {
		t.Fatalf("refused change was stored: %+v err=%v", row, err)
	}
}
