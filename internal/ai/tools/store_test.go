package tools

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestStoreAdapterWritesAudits(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	deps := WithStore(Dependencies{}, storage, nil)
	if err := deps.Audit(context.Background(), AuditEntry{SessionID: "s", Kind: "exec", Payload: map[string]any{"tool": "read_file"}, ExitCode: 0, DurationMS: 5}); err != nil {
		t.Fatal(err)
	}
	rows, err := storage.AuditQuery(context.Background(), store.AuditQuery{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if rows[0].Source != "ai" || rows[0].ExitCode == nil || *rows[0].ExitCode != 0 || rows[0].DurationMS == nil {
		t.Fatalf("row=%+v", rows[0])
	}
}
