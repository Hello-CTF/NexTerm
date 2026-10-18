package store

import (
	"context"
	"testing"
)

func TestGroupDeleteRollsBackWhenTombstoneCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	root, err := db.GroupCreate(ctx, GroupInput{Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := db.GroupCreate(ctx, GroupInput{Name: "child", ParentID: &root.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.GORM().Exec(`CREATE TRIGGER reject_group_tombstones BEFORE INSERT ON sync_tombstone
		WHEN NEW.kind = 'group' BEGIN SELECT RAISE(ABORT, 'blocked'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GroupDelete(ctx, root.ID); err == nil {
		t.Fatal("group deletion succeeded without tombstones")
	}
	for _, id := range []string{root.ID, child.ID} {
		if _, err := db.GroupGet(ctx, id); err != nil {
			t.Fatalf("group %s was deleted despite tombstone rollback: %v", id, err)
		}
	}
}
