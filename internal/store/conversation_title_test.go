package store

import (
	"context"
	"testing"
)

func TestConvRenameIfTitleCompareAndSwaps(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	conversation, err := db.ConvCreate(ctx, "原始标题", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := db.ConvRenameIfTitle(ctx, conversation.ID, "原始标题", "新标题")
	if err != nil || !renamed {
		t.Fatalf("matching rename = %v err=%v", renamed, err)
	}
	row, err := db.ConvGet(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Title != "新标题" {
		t.Fatalf("title = %q", row.Title)
	}
	renamed, err = db.ConvRenameIfTitle(ctx, conversation.ID, "原始标题", "过期标题")
	if err != nil {
		t.Fatal(err)
	}
	if renamed {
		t.Fatal("stale expected title must not rename")
	}
	row, err = db.ConvGet(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Title != "新标题" {
		t.Fatalf("title after stale rename = %q", row.Title)
	}
	renamed, err = db.ConvRenameIfTitle(ctx, "missing-conversation", "任意", "标题")
	if err != nil {
		t.Fatal(err)
	}
	if renamed {
		t.Fatal("missing conversation must report no rename")
	}
}
