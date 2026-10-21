package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestRosterRealStoreLifecycle proves the RosterStore contract is satisfied by the
// real *store.Store end to end: provisioning-first creation, cross-job resolution,
// two-phase release, and authoritative-gone resolution.
func TestRosterRealStoreLifecycle(t *testing.T) {
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	probe := newFakeRosterProbe()
	probe.set("handle-a", RosterRuntimeConnected, "")
	roster := newTestRoster(db, probe)

	entry, err := roster.Provision(ctx, "conv-1", "handle-a", "alias-a", "tab-1", map[string]any{"origin": "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != RosterStateProvisioning {
		t.Fatalf("state=%s", entry.State)
	}
	attachment, err := roster.Attach(ctx, "handle-a", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionConnected || attachment.Entry.JobID != "job-1" {
		t.Fatalf("attach = %+v", attachment)
	}
	if _, err := roster.RequestRelease(ctx, "handle-a", rosterIdentityOf(entry)); err != nil {
		t.Fatal(err)
	}
	confirmed, err := roster.ConfirmRelease(ctx, "handle-a", rosterIdentityOf(entry))
	if err != nil || confirmed.State != RosterStateReleased {
		t.Fatalf("confirm = %+v err=%v", confirmed, err)
	}
	attachment, err = roster.Attach(ctx, "handle-a", "job-2")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionGone {
		t.Fatalf("resolution=%s", attachment.Resolution)
	}
	report, err := roster.Reconcile(ctx, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 0 {
		t.Fatalf("terminal rows were re-examined: %+v", report.Entries)
	}
}

// TestRosterRealStoreRestartRecoversExactIdentity proves recovery across restart:
// the reloaded row keeps its ID+CreatedAt+Incarnation triple, so exact-identity
// mutations still land and stale identities are rejected.
func TestRosterRealStoreRestartRecoversExactIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roster.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	probe := newFakeRosterProbe()
	roster := newTestRoster(db, probe)
	entry, err := roster.Provision(ctx, "conv-1", "handle-a", "", "tab-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	roster = newTestRoster(reopened, probe)

	loaded, err := reopened.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != entry.ID || loaded.CreatedAt != entry.CreatedAt || loaded.Incarnation != entry.Incarnation {
		t.Fatalf("recovered identity = %+v, want %+v", loaded, entry)
	}
	// Recovery uses the caller's previously saved identity, not a fresh read:
	// the stale-saved triple must still land, and a wrong one must not.
	activated, err := roster.Activate(ctx, entry.ID, rosterIdentityOf(entry), "job-after-restart")
	if err != nil || activated.State != RosterStateActive || activated.JobID != "job-after-restart" {
		t.Fatalf("post-restart activate = %+v err=%v", activated, err)
	}
	if _, err := roster.Activate(ctx, entry.ID, RosterIdentity{CreatedAt: entry.CreatedAt, Incarnation: "stale"}, "job-stale"); !errors.Is(err, ErrRosterIdentityConflict) {
		t.Fatalf("stale identity activate err=%v", err)
	}
	if _, err := reopened.AgentRosterUpdate(ctx, loaded.ID, store.AgentRosterIdentity{CreatedAt: loaded.CreatedAt, Incarnation: "stale"}, map[string]any{"state": "gone"}); err == nil {
		t.Fatal("stale identity wrote after restart")
	}
	stored, err := reopened.AgentRosterGet(ctx, loaded.ID)
	if err != nil || stored.State != string(RosterStateActive) || stored.JobID != "job-after-restart" {
		t.Fatalf("stale caller disturbed the row: %+v err=%v", stored, err)
	}
}
