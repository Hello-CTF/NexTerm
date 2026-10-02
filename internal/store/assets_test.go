package store

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestBuiltinAssetLifecycle(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	first, err := db.AssetEnsureBuiltinLocal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.AssetEnsureBuiltinLocal(ctx)
	if err != nil || second.CreatedAt != first.CreatedAt {
		t.Fatalf("seed not idempotent: %+v err=%v", second, err)
	}
	if !first.Builtin || first.Kind != "local" || first.Sort != -1 {
		t.Fatalf("unexpected built-in asset: %+v", first)
	}
	_, err = db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: " web-01 ", Host: ptr("10.0.0.1"), OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	assets, err := db.AssetList(ctx, false)
	if err != nil || len(assets) != 2 || assets[0].ID != BuiltinLocalAssetID {
		t.Fatalf("built-in not first: %+v err=%v", assets, err)
	}
	requireCode(t, db.AssetDelete(ctx, BuiltinLocalAssetID), ipc.CodeBadParam)
	if _, err := db.DB().Exec("UPDATE asset SET deleted_at=1 WHERE id=?", BuiltinLocalAssetID); err != nil {
		t.Fatal(err)
	}
	revived, err := db.AssetEnsureBuiltinLocal(ctx)
	if err != nil || revived.DeletedAt != nil {
		t.Fatalf("revive failed: %+v err=%v", revived, err)
	}
}

func TestGroupCycleAndAssetSoftDeleteSearch(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	parent, err := db.GroupCreate(ctx, GroupInput{Name: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := db.GroupCreate(ctx, GroupInput{Name: "child", ParentID: &parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.GroupUpdate(ctx, parent.ID, GroupPatch{ParentID: Value(child.ID)})
	requireCode(t, err, ipc.CodeBadParam)

	asset, err := db.AssetCreate(ctx, AssetInput{GroupID: &child.ID, Kind: "ssh", Name: "Web", Host: ptr("Example.ORG"), Username: ptr("root"), OptionsJSON: "{}", Tags: "production"})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := db.AssetSearch(ctx, "example.org")
	if err != nil || len(matches) != 1 {
		t.Fatalf("search results=%v err=%v", matches, err)
	}
	updated, err := db.AssetUpdate(ctx, asset.ID, AssetPatch{Host: Null[string](), Name: ptr(" renamed ")})
	if err != nil || updated.Host != nil || updated.Name != "renamed" {
		t.Fatalf("patch result=%+v err=%v", updated, err)
	}
	if err := db.AssetDelete(ctx, asset.ID); err != nil {
		t.Fatal(err)
	}
	active, _ := db.AssetList(ctx, false)
	all, _ := db.AssetList(ctx, true)
	if len(active) != 0 || len(all) != 1 || all[0].DeletedAt == nil {
		t.Fatalf("soft delete active=%d all=%+v", len(active), all)
	}
}

func TestSyncUpsertsPreserveLocalFacts(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	row := AssetRow{ID: ids.New(), Kind: "ssh", Name: "origin", OptionsJSON: "{}", CreatedAt: 123, UpdatedAt: 456}
	created, err := db.AssetUpsert(ctx, row)
	if err != nil || !created {
		t.Fatalf("insert created=%v err=%v", created, err)
	}
	row.Name = "updated"
	row.CreatedAt = 999
	row.DeletedAt = ptr(int64(777))
	created, err = db.AssetUpsert(ctx, row)
	if err != nil || created {
		t.Fatalf("update created=%v err=%v", created, err)
	}
	got, err := db.AssetGet(ctx, row.ID)
	if err != nil || got.CreatedAt != 123 || got.Name != "updated" || got.DeletedAt == nil {
		t.Fatalf("upsert result=%+v err=%v", got, err)
	}
	row.ID = BuiltinLocalAssetID
	_, err = db.AssetUpsert(ctx, row)
	requireCode(t, err, ipc.CodeBadParam)

	groupCreated, err := db.GroupUpsert(ctx, ids.New(), nil, " group ", 1, 100, 200)
	if err != nil || !groupCreated {
		t.Fatalf("group upsert created=%v err=%v", groupCreated, err)
	}
}

func TestCredentialUsageAndForeignKeyCleanup(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	credentialID, err := db.CredentialPut(ctx, CredentialInput{Name: "login", Kind: "password", Nonce: make([]byte, 12), Blob: []byte{1, 2, 3}, KEKHint: "master:0"})
	if err != nil {
		t.Fatal(err)
	}
	asset, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "web", CredID: &credentialID, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := db.CredentialUsage(ctx, credentialID)
	if err != nil || len(usage) != 1 || usage[0].ID != asset.ID {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
	if err := db.CredentialDelete(ctx, credentialID); err != nil {
		t.Fatal(err)
	}
	asset, err = db.AssetGet(ctx, asset.ID)
	if err != nil || asset.CredID != nil {
		t.Fatalf("credential reference not cleared: %+v err=%v", asset, err)
	}
}
