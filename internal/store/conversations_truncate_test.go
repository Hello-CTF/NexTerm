package store

import (
	"context"
	"testing"
)

func TestMsgTruncateAfter(t *testing.T) {
	storage, err := OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	conversation, err := storage.ConvCreate(context.Background(), "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, content := range []string{"one", "two", "three"} {
		if err := storage.MsgInsert(ctx, conversation.ID, "user", map[string]any{"role": "user", "content": content}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := storage.MsgList(ctx, conversation.ID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows = %d err=%v", len(rows), err)
	}
	if err := storage.MsgTruncateAfter(ctx, conversation.ID, rows[1].ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := storage.MsgList(ctx, conversation.ID)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("remaining = %d err=%v", len(remaining), err)
	}
	if remaining[0].ContentJSON != rows[0].ContentJSON || remaining[1].ContentJSON != rows[1].ContentJSON {
		t.Fatalf("remaining rows = %+v", remaining)
	}
	if err := storage.MsgTruncateAfter(ctx, conversation.ID, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	remaining, err = storage.MsgList(ctx, conversation.ID)
	if err != nil || len(remaining) != 1 {
		t.Fatalf("remaining = %d err=%v", len(remaining), err)
	}
	if err := storage.MsgTruncateAfter(ctx, conversation.ID, "missing"); err == nil {
		t.Fatal("truncating at a missing message succeeded")
	}
	if err := storage.MsgTruncateAfter(ctx, "missing-conversation", rows[0].ID); err == nil {
		t.Fatal("truncating in a missing conversation succeeded")
	}
}
