package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

func TestGroupDeleteRecordsDescendantTombstonesAndPreservesAssetsDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	root, err := db.GroupCreate(ctx, store.GroupInput{Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := db.GroupCreate(ctx, store.GroupInput{Name: "child", ParentID: &root.ID})
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := db.GroupCreate(ctx, store.GroupInput{Name: "grandchild", ParentID: &child.ID})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := db.GroupCreate(ctx, store.GroupInput{Name: "unrelated"})
	if err != nil {
		t.Fatal(err)
	}
	asset, err := db.AssetCreate(ctx, store.AssetInput{GroupID: &grandchild.ID, Kind: "ssh", Name: "host", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	snippet, err := db.SnippetCreate(ctx, "command", "echo ok", &grandchild.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.GroupDelete(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	groups, err := db.GroupList(ctx)
	if err != nil || len(groups) != 1 || groups[0].ID != unrelated.ID {
		t.Fatalf("groups after parent deletion = %+v, %v", groups, err)
	}
	for _, group := range []store.AssetGroupRow{root, child, grandchild} {
		var tombstone store.SyncTombstone
		if err := db.GORM().WithContext(ctx).Where("id = ?", group.ID).First(&tombstone).Error; err != nil {
			t.Fatalf("group %s tombstone: %v", group.ID, err)
		}
		if tombstone.Kind != "group" || tombstone.DeletedAt < group.UpdatedAt {
			t.Fatalf("group %s tombstone = %+v", group.ID, tombstone)
		}
	}
	retained, err := db.AssetGet(ctx, asset.ID)
	if err != nil || retained.GroupID != nil || retained.DeletedAt != nil {
		t.Fatalf("asset after parent deletion = %+v, %v", retained, err)
	}
	retainedSnippet, err := db.SnippetGet(ctx, snippet.ID)
	if err != nil || retainedSnippet.GroupID != nil {
		t.Fatalf("snippet after parent deletion = %+v, %v", retainedSnippet, err)
	}
}

func TestGroupReferencesRequireMatchingOwnerDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "user-a")
	ctxB := ipc.WithUserID(context.Background(), "user-b")
	groupA, err := db.GroupCreate(ctxA, store.GroupInput{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	groupB, err := db.GroupCreate(ctxB, store.GroupInput{Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	childA, err := db.GroupCreate(ctxA, store.GroupInput{Name: "a-child", ParentID: &groupA.ID})
	if err != nil {
		t.Fatal(err)
	}
	assetA, err := db.AssetCreate(ctxA, store.AssetInput{GroupID: &groupA.ID, Kind: "ssh", Name: "a-host", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	snippetA, err := db.SnippetCreate(ctxA, "a-command", "echo a", &groupA.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertCode := func(err error, code ipc.Code) {
		t.Helper()
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != code {
			t.Fatalf("error = %v, want %s", err, code)
		}
	}
	err = db.GroupDelete(ctxB, groupA.ID)
	assertCode(err, ipc.CodeNotFound)
	if retained, err := db.GroupGet(ctxA, childA.ID); err != nil || retained.ParentID == nil || *retained.ParentID != groupA.ID {
		t.Fatalf("unauthorized delete changed A's child: %+v, %v", retained, err)
	}
	if retained, err := db.AssetGet(ctxA, assetA.ID); err != nil || retained.GroupID == nil || *retained.GroupID != groupA.ID {
		t.Fatalf("unauthorized delete changed A's asset: %+v, %v", retained, err)
	}
	if retained, err := db.SnippetGet(ctxA, snippetA.ID); err != nil || retained.GroupID == nil || *retained.GroupID != groupA.ID {
		t.Fatalf("unauthorized delete changed A's snippet: %+v, %v", retained, err)
	}
	_, err = db.GroupCreate(ctxB, store.GroupInput{Name: "foreign child", ParentID: &groupA.ID})
	assertCode(err, ipc.CodeForbidden)
	_, err = db.GroupUpdate(ctxB, groupB.ID, store.GroupPatch{ParentID: store.Value(groupA.ID)})
	assertCode(err, ipc.CodeForbidden)
	_, err = db.GroupUpsert(ctxB, ids.New(), &groupA.ID, "foreign child", 0, 1, 1)
	assertCode(err, ipc.CodeForbidden)
	_, err = db.AssetCreate(ctxB, store.AssetInput{GroupID: &groupA.ID, Kind: "ssh", Name: "foreign asset", OptionsJSON: "{}"})
	assertCode(err, ipc.CodeForbidden)
	assetB, err := db.AssetCreate(ctxB, store.AssetInput{GroupID: &groupB.ID, Kind: "ssh", Name: "own asset", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.AssetUpdate(ctxB, assetB.ID, store.AssetPatch{GroupID: store.Value(groupA.ID)})
	assertCode(err, ipc.CodeForbidden)
	_, err = db.AssetUpsert(ctxB, store.AssetRow{ID: ids.New(), GroupID: &groupA.ID, Kind: "ssh", Name: "foreign sync asset", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 1})
	assertCode(err, ipc.CodeForbidden)
	_, err = db.SnippetCreate(ctxB, "foreign snippet", "echo bad", &groupA.ID, 0)
	assertCode(err, ipc.CodeForbidden)
	snippetB, err := db.SnippetCreate(ctxB, "own snippet", "echo ok", &groupB.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = db.SnippetUpdateFields(ctxB, snippetB.ID, store.SnippetPatch{Name: "own snippet", Body: "echo ok", GroupID: store.Value(groupA.ID)})
	assertCode(err, ipc.CodeForbidden)
	missing := ids.New()
	_, err = db.GroupCreate(ctxB, store.GroupInput{Name: "missing parent", ParentID: &missing})
	assertCode(err, ipc.CodeBadParam)
	_, err = db.AssetCreate(ctxB, store.AssetInput{GroupID: &missing, Kind: "ssh", Name: "missing group", OptionsJSON: "{}"})
	assertCode(err, ipc.CodeBadParam)
	unchanged, err := db.AssetGet(ctxB, assetB.ID)
	if err != nil || unchanged.GroupID == nil || *unchanged.GroupID != groupB.ID {
		t.Fatalf("failed cross-user asset move changed group: %+v, %v", unchanged, err)
	}

	// Older rows may contain invalid cross-owner references. Deleting A's group
	// must detach those references without cascading into B's group or asset.
	foreignChildID := ids.New()
	if err := db.GORM().WithContext(ctxB).Create(&store.AssetGroupRow{
		ID: foreignChildID, ParentID: &groupA.ID, Name: "old cross-owner child", CreatedAt: 1, UpdatedAt: 1, OwnerID: "user-b",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GORM().WithContext(ctxB).Model(&store.AssetRow{}).Where("id = ?", assetB.ID).Update("group_id", groupA.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GORM().WithContext(ctxB).Model(&store.SnippetRow{}).Where("id = ?", snippetB.ID).Update("group_id", groupA.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GroupDelete(ctxA, groupA.ID); err != nil {
		t.Fatal(err)
	}
	foreignChild, err := db.GroupGet(ctxB, foreignChildID)
	if err != nil || foreignChild.ParentID != nil {
		t.Fatalf("foreign child lost on owner deletion: %+v, %v", foreignChild, err)
	}
	retained, err := db.AssetGet(ctxB, assetB.ID)
	if err != nil || retained.GroupID != nil || retained.DeletedAt != nil {
		t.Fatalf("foreign asset lost on owner deletion: %+v, %v", retained, err)
	}
	retainedSnippet, err := db.SnippetGet(ctxB, snippetB.ID)
	if err != nil || retainedSnippet.GroupID != nil {
		t.Fatalf("foreign snippet lost on owner deletion: %+v, %v", retainedSnippet, err)
	}
	var tombstone store.SyncTombstone
	if err := db.GORM().WithContext(ctxB).Where("id = ? AND owner_id = ?", foreignChildID, "user-b").First(&tombstone).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("foreign child tombstone = %+v, %v", tombstone, err)
	}
}
