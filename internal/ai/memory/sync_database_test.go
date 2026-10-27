package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// Synced writes keep the peer revision, are bound to the caller's snapshot and stay inside their scope;
// a user deletion leaves a sync tombstone in the shared database.
func TestMemorySyncWritesDatabase(t *testing.T) {
	ctx := context.Background()
	store, db := openMemoryDatabase(t)
	other := Scope{Tenant: "user", Subject: "someone-else"}
	entry := Entry{ID: "01J00000000000000000000MEM", Topic: "deploy", Content: "token=abc123 for api", CreatedAt: 10, UpdatedAt: 20}
	if err := store.PutSynced(ctx, testScope, entry, nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, testScope, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdatedAt != 20 || got.CreatedAt != 10 || got.Version != 1 || !got.Redacted || got.Content == entry.Content {
		t.Fatalf("synced entry=%+v", got)
	}
	if err := store.PutSynced(ctx, testScope, entry, nil); !errors.Is(err, ErrSyncConflict) {
		t.Fatalf("a write expecting no entry over an existing one: %v", err)
	}
	if err := store.PutSynced(ctx, other, entry, nil); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("another scope's write over the entry: %v", err)
	}
	next := entry
	next.Content, next.UpdatedAt = "api moved to port 9090", 30
	if err := store.PutSynced(ctx, testScope, next, &got); err != nil {
		t.Fatal(err)
	}
	if got, err = store.Get(ctx, testScope, entry.ID); err != nil || got.Version != 2 || got.Content != next.Content {
		t.Fatalf("updated entry=%+v err=%v", got, err)
	}
	if removed, err := store.DeleteSynced(ctx, testScope, entry.ID, 25); err != nil || removed {
		t.Fatalf("an older deletion removed=%v err=%v", removed, err)
	}
	if removed, err := store.DeleteSynced(ctx, testScope, entry.ID, 40); err != nil || !removed {
		t.Fatalf("a newer deletion removed=%v err=%v", removed, err)
	}

	created, err := store.Create(ctx, testScope, CreateInput{Topic: "notes", Content: "remember the backup window"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, testScope, created.ID, created.Version); err != nil {
		t.Fatal(err)
	}
	var tombstone model.SyncTombstone
	if err := db.WithContext(ctx).Where("id = ?", created.ID).First(&tombstone).Error; err != nil || tombstone.Kind != SyncTombstoneKind {
		t.Fatalf("user deletion tombstone=%+v err=%v", tombstone, err)
	}
}
