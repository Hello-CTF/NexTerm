package store

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func rosterCtx(owner string) context.Context {
	return ipc.WithUserID(context.Background(), owner)
}

func provisionRoster(t *testing.T, db *Store, ctx context.Context, conversationID, handle string) AgentRosterRow {
	t.Helper()
	row, err := db.AgentRosterProvision(ctx, AgentRosterRow{
		ConversationID: conversationID, Handle: handle, Alias: handle + "-alias",
		TabID: "tab-preassigned", State: "provisioning", Incarnation: "inc-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestAgentRosterProvisionAndGetRoundTrip(t *testing.T) {
	db := testStore(t)
	ctx := rosterCtx("owner-a")
	row := provisionRoster(t, db, ctx, "conv-1", "handle-a")
	if row.ID == "" || row.OwnerID != "owner-a" {
		t.Fatalf("provision did not stamp identity: %+v", row)
	}
	if row.TabID != "tab-preassigned" || row.State != "provisioning" || row.Incarnation != "inc-1" {
		t.Fatalf("provisioning-first fields lost: %+v", row)
	}
	if row.MetadataJSON != "{}" || row.CreatedAt == 0 || row.UpdatedAt == 0 {
		t.Fatalf("defaults not applied: %+v", row)
	}
	loaded, err := db.AgentRosterGet(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != row {
		t.Fatalf("get = %+v, want %+v", loaded, row)
	}
}

func TestAgentRosterProvisionValidation(t *testing.T) {
	db := testStore(t)
	ctx := rosterCtx("owner-a")
	for name, row := range map[string]AgentRosterRow{
		"empty handle":      {ConversationID: "c", State: "provisioning", Incarnation: "i"},
		"empty state":       {ConversationID: "c", Handle: "h", Incarnation: "i"},
		"empty incarnation": {ConversationID: "c", Handle: "h", State: "provisioning"},
	} {
		if _, err := db.AgentRosterProvision(ctx, row); err == nil {
			t.Fatalf("%s was accepted", name)
		} else {
			requireCode(t, err, ipc.CodeBadParam)
		}
	}
}

func TestAgentRosterHandlePermanentPerOwner(t *testing.T) {
	db := testStore(t)
	ctxA := rosterCtx("owner-a")
	ctxB := rosterCtx("owner-b")
	provisionRoster(t, db, ctxA, "conv-1", "shared-name")
	// The same handle under a different owner is a different namespace and must succeed.
	provisionRoster(t, db, ctxB, "conv-9", "shared-name")
	// The same handle under the same owner is a conflict: handles are never reused.
	_, err := db.AgentRosterProvision(ctxA, AgentRosterRow{
		ConversationID: "conv-2", Handle: "shared-name", State: "provisioning", Incarnation: "inc-2",
	})
	requireCode(t, err, ipc.Code("conflict"))
}

func TestAgentRosterOwnerIsolation(t *testing.T) {
	db := testStore(t)
	ctxA := rosterCtx("owner-a")
	ctxB := rosterCtx("owner-b")
	row := provisionRoster(t, db, ctxA, "conv-1", "handle-a")

	if _, err := db.AgentRosterGet(ctxB, row.ID); err == nil {
		t.Fatal("cross-owner get succeeded")
	} else {
		requireCode(t, err, ipc.CodeNotFound)
	}
	if _, err := db.AgentRosterGetByHandle(ctxB, "handle-a"); err == nil {
		t.Fatal("cross-owner handle resolution succeeded")
	}
	if _, err := db.AgentRosterUpdate(ctxB, row.ID, row.Identity(), map[string]any{"state": "gone"}); err == nil {
		t.Fatal("cross-owner update succeeded")
	} else {
		requireCode(t, err, ipc.CodeNotFound)
	}
	rowsA, err := db.AgentRosterList(ctxA, "")
	if err != nil {
		t.Fatal(err)
	}
	rowsB, err := db.AgentRosterList(ctxB, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsA) != 1 || len(rowsB) != 0 {
		t.Fatalf("owner lists = %d/%d, want 1/0", len(rowsA), len(rowsB))
	}
	// The untouched row is still intact for its owner.
	loaded, err := db.AgentRosterGet(ctxA, row.ID)
	if err != nil || loaded.State != "provisioning" {
		t.Fatalf("row mutated by cross-owner attempts: %+v err=%v", loaded, err)
	}
}

func TestAgentRosterCrossJobResolution(t *testing.T) {
	db := testStore(t)
	ctx := rosterCtx("owner-a")
	row := provisionRoster(t, db, ctx, "conv-1", "handle-a")

	// Job 1 binds, job 2 binds later: the handle resolves to the latest binding.
	updated, err := db.AgentRosterUpdate(ctx, row.ID, row.Identity(), map[string]any{"state": "active", "job_id": "job-1"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err = db.AgentRosterUpdate(ctx, row.ID, updated.Identity(), map[string]any{"job_id": "job-2"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := db.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.JobID != "job-2" || resolved.State != "active" {
		t.Fatalf("cross-job resolution = %+v", resolved)
	}
}

func TestAgentRosterExactIdentityUpdate(t *testing.T) {
	db := testStore(t)
	ctx := rosterCtx("owner-a")
	row := provisionRoster(t, db, ctx, "conv-1", "handle-a")

	// Wrong incarnation must not write.
	if _, err := db.AgentRosterUpdate(ctx, row.ID, AgentRosterIdentity{CreatedAt: row.CreatedAt, Incarnation: "inc-other"}, map[string]any{"state": "gone"}); !errors.Is(err, ErrAgentRosterIdentityConflict) {
		t.Fatalf("wrong incarnation err=%v", err)
	}
	// Wrong CreatedAt must not write.
	if _, err := db.AgentRosterUpdate(ctx, row.ID, AgentRosterIdentity{CreatedAt: row.CreatedAt + 1, Incarnation: row.Incarnation}, map[string]any{"state": "gone"}); !errors.Is(err, ErrAgentRosterIdentityConflict) {
		t.Fatalf("wrong created_at err=%v", err)
	}
	// Missing row reports not-found rather than an identity conflict.
	if _, err := db.AgentRosterUpdate(ctx, "missing", row.Identity(), map[string]any{"state": "gone"}); err == nil {
		t.Fatal("update on missing row succeeded")
	} else {
		requireCode(t, err, ipc.CodeNotFound)
	}
	// Nothing above may have mutated the row.
	loaded, err := db.AgentRosterGet(ctx, row.ID)
	if err != nil || loaded.State != "provisioning" {
		t.Fatalf("row mutated by rejected updates: %+v err=%v", loaded, err)
	}
	// The exact triple writes.
	updated, err := db.AgentRosterUpdate(ctx, row.ID, row.Identity(), map[string]any{"state": "active"})
	if err != nil || updated.State != "active" {
		t.Fatalf("exact-identity update = %+v err=%v", updated, err)
	}
}

func TestAgentRosterCompareAndSwapState(t *testing.T) {
	db := testStore(t)
	ctx := rosterCtx("owner-a")
	row := provisionRoster(t, db, ctx, "conv-1", "handle-a")

	// Wrong expected state: no write.
	if _, err := db.AgentRosterCompareAndSwapState(ctx, row.ID, row.Identity(), "active", map[string]any{"state": "gone"}); !errors.Is(err, ErrAgentRosterStateConflict) {
		t.Fatalf("wrong expected state err=%v", err)
	}
	// Wrong identity: no write.
	if _, err := db.AgentRosterCompareAndSwapState(ctx, row.ID, AgentRosterIdentity{CreatedAt: row.CreatedAt, Incarnation: "other"}, "provisioning", map[string]any{"state": "gone"}); !errors.Is(err, ErrAgentRosterIdentityConflict) {
		t.Fatalf("wrong identity err=%v", err)
	}
	loaded, err := db.AgentRosterGet(ctx, row.ID)
	if err != nil || loaded.State != "provisioning" {
		t.Fatalf("rejected CAS wrote: %+v err=%v", loaded, err)
	}
	// Correct identity and state: the write lands.
	updated, err := db.AgentRosterCompareAndSwapState(ctx, row.ID, row.Identity(), "provisioning", map[string]any{"state": "active"})
	if err != nil || updated.State != "active" {
		t.Fatalf("cas update = %+v err=%v", updated, err)
	}
	// Missing row reports not-found.
	if _, err := db.AgentRosterCompareAndSwapState(ctx, "missing", row.Identity(), "provisioning", map[string]any{"state": "active"}); err == nil {
		t.Fatal("cas on missing row succeeded")
	} else {
		requireCode(t, err, ipc.CodeNotFound)
	}
}

func TestAgentRosterTerminalRowsAreTombstones(t *testing.T) {
	db := testStore(t)
	ctx := rosterCtx("owner-a")
	row := provisionRoster(t, db, ctx, "conv-1", "handle-a")
	// A terminal (gone) row is retained as a tombstone: it stays listable as
	// orphan evidence and the permanent handle cannot be reused.
	if _, err := db.AgentRosterUpdate(ctx, row.ID, row.Identity(), map[string]any{"state": "gone"}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.AgentRosterList(ctx, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != "gone" {
		t.Fatalf("tombstone vanished: %+v", rows)
	}
	if _, err := db.AgentRosterProvision(ctx, AgentRosterRow{
		ConversationID: "conv-2", Handle: "handle-a", State: "provisioning", Incarnation: "inc-2",
	}); err == nil {
		t.Fatal("permanent handle was reused after the row went terminal")
	} else {
		requireCode(t, err, ipc.Code("conflict"))
	}
	resolved, err := db.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil || resolved.State != "gone" {
		t.Fatalf("handle resolves to the tombstone: %+v err=%v", resolved, err)
	}
}

func TestAgentRosterListScopesByConversation(t *testing.T) {
	db := testStore(t)
	ctx := rosterCtx("owner-a")
	provisionRoster(t, db, ctx, "conv-1", "handle-a")
	provisionRoster(t, db, ctx, "conv-2", "handle-b")
	rows, err := db.AgentRosterList(ctx, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Handle != "handle-a" {
		t.Fatalf("conversation list = %+v", rows)
	}
	all, err := db.AgentRosterList(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("owner-wide list = %d rows, want 2", len(all))
	}
}
