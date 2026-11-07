package store_test

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func assetOrder(t *testing.T, db *store.Store, groupID *string) []string {
	t.Helper()
	rows, err := db.AssetList(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, row := range rows {
		if row.Builtin || row.Kind == store.AssetKindDevice {
			continue
		}
		if (groupID == nil) != (row.GroupID == nil) || (groupID != nil && *groupID != *row.GroupID) {
			continue
		}
		names = append(names, row.Name)
	}
	return names
}

func createOrderedAsset(t *testing.T, db *store.Store, name string, groupID *string) store.AssetRow {
	t.Helper()
	ctx := context.Background()
	sort, err := db.AssetTopSort(ctx, groupID)
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: name, GroupID: groupID, OptionsJSON: "{}", Sort: sort})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func equalNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestAssetOrderNewFirstAndMoveDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	if _, err := db.AssetEnsureBuiltinLocal(ctx); err != nil {
		t.Fatal(err)
	}
	a := createOrderedAsset(t, db, "a", nil)
	b := createOrderedAsset(t, db, "b", nil)
	c := createOrderedAsset(t, db, "c", nil)
	if got := assetOrder(t, db, nil); !equalNames(got, []string{"c", "b", "a"}) {
		t.Fatalf("new assets must go first: %v", got)
	}
	rows, err := db.AssetList(ctx, false)
	if err != nil || len(rows) == 0 || !rows[0].Builtin {
		t.Fatalf("the built-in local asset must stay first: %+v err=%v", rows, err)
	}

	if _, err := db.AssetMove(ctx, a.ID, nil, c.ID); err != nil {
		t.Fatal(err)
	}
	if got := assetOrder(t, db, nil); !equalNames(got, []string{"a", "c", "b"}) {
		t.Fatalf("move before c: %v", got)
	}
	if _, err := db.AssetMove(ctx, a.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := assetOrder(t, db, nil); !equalNames(got, []string{"c", "b", "a"}) {
		t.Fatalf("move to the end: %v", got)
	}

	group, err := db.GroupCreate(ctx, store.GroupInput{Name: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	x := createOrderedAsset(t, db, "x", &group.ID)
	moved, err := db.AssetMove(ctx, b.ID, &group.ID, x.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.GroupID == nil || *moved.GroupID != group.ID {
		t.Fatalf("move into a group must set the group: %+v", moved)
	}
	if got := assetOrder(t, db, &group.ID); !equalNames(got, []string{"b", "x"}) {
		t.Fatalf("group order: %v", got)
	}
	if got := assetOrder(t, db, nil); !equalNames(got, []string{"c", "a"}) {
		t.Fatalf("root order after moving out: %v", got)
	}

	if _, err := db.AssetMove(ctx, store.BuiltinLocalAssetID, nil, ""); err == nil {
		t.Fatal("the built-in local asset must not be reordered")
	}
	if _, err := db.AssetMove(ctx, a.ID, nil, x.ID); err == nil {
		t.Fatal("a beforeId outside the target group must be rejected")
	}
	if _, err := db.AssetMove(ctx, a.ID, nil, a.ID); err == nil {
		t.Fatal("an asset cannot be placed before itself")
	}
}

func TestGroupMoveReordersSiblingsDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	var ids []string
	for _, name := range []string{"a", "b", "c"} {
		group, err := db.GroupCreate(ctx, store.GroupInput{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, group.ID)
	}
	child, err := db.GroupCreate(ctx, store.GroupInput{Name: "child", ParentID: &ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.GroupMove(ctx, ids[2], ids[0]); err != nil {
		t.Fatal(err)
	}
	groups, err := db.GroupList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var roots []string
	for _, group := range groups {
		if group.ParentID == nil {
			roots = append(roots, group.Name)
		}
	}
	if !equalNames(roots, []string{"c", "a", "b"}) {
		t.Fatalf("root groups = %v", roots)
	}
	if _, err := db.GroupMove(ctx, child.ID, ids[1]); err == nil {
		t.Fatal("a group can only be placed among the groups that share its parent")
	}
}
