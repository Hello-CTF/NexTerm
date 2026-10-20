package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestAgentRosterContractsDatabase exercises the roster CRUD, owner
// isolation, handle uniqueness, and compare-and-swap contracts against every
// real configured backend (PostgreSQL/MySQL/MariaDB via NEXTERM_TEST_DB_*),
// not just SQLite.
func TestAgentRosterContractsDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "roster-owner-a")
	ctxB := ipc.WithUserID(context.Background(), "roster-owner-b")

	row, err := db.AgentRosterProvision(ctxA, store.AgentRosterRow{
		ConversationID: "conv-1", Handle: "handle-a", Alias: "alias-a",
		TabID: "tab-a", State: "provisioning", Incarnation: "inc-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.OwnerID != "roster-owner-a" || row.TabID != "tab-a" {
		t.Fatalf("provision = %+v", row)
	}

	// Handle uniqueness is per owner on every backend.
	if _, err := db.AgentRosterProvision(ctxA, store.AgentRosterRow{
		ConversationID: "conv-2", Handle: "handle-a", State: "provisioning", Incarnation: "inc-2",
	}); err == nil {
		t.Fatal("duplicate handle accepted")
	}
	if _, err := db.AgentRosterProvision(ctxB, store.AgentRosterRow{
		ConversationID: "conv-9", Handle: "handle-b", State: "provisioning", Incarnation: "inc-3",
	}); err != nil {
		t.Fatalf("cross-owner handle rejected: %v", err)
	}

	// Owner isolation on reads.
	if _, err := db.AgentRosterGet(ctxB, row.ID); err == nil {
		t.Fatal("cross-owner get succeeded")
	}
	if _, err := db.AgentRosterGetByHandle(ctxB, "handle-a"); err == nil {
		t.Fatal("cross-owner handle resolution returned the wrong owner's row")
	}

	// Compare-and-swap: wrong state is a conflict, right state lands.
	if _, err := db.AgentRosterCompareAndSwapState(ctxA, row.ID, row.Identity(), "active", map[string]any{"state": "gone"}); !errors.Is(err, store.ErrAgentRosterStateConflict) {
		t.Fatalf("cas wrong state err=%v", err)
	}
	activated, err := db.AgentRosterCompareAndSwapState(ctxA, row.ID, row.Identity(), "provisioning", map[string]any{"state": "active", "job_id": "job-1"})
	if err != nil {
		t.Fatal(err)
	}
	if activated.State != "active" || activated.JobID != "job-1" {
		t.Fatalf("cas activate = %+v", activated)
	}

	// Wrong identity is rejected on both update paths.
	stale := store.AgentRosterIdentity{CreatedAt: row.CreatedAt, Incarnation: "stale"}
	if _, err := db.AgentRosterUpdate(ctxA, row.ID, stale, map[string]any{"state": "gone"}); !errors.Is(err, store.ErrAgentRosterIdentityConflict) {
		t.Fatalf("stale update err=%v", err)
	}
	if _, err := db.AgentRosterCompareAndSwapState(ctxA, row.ID, stale, "active", map[string]any{"state": "gone"}); !errors.Is(err, store.ErrAgentRosterIdentityConflict) {
		t.Fatalf("stale cas err=%v", err)
	}

	// The row survives as a listable tombstone; the handle stays non-reusable.
	gone, err := db.AgentRosterCompareAndSwapState(ctxA, row.ID, activated.Identity(), "active", map[string]any{"state": "gone"})
	if err != nil || gone.State != "gone" {
		t.Fatalf("cas gone = %+v err=%v", gone, err)
	}
	rows, err := db.AgentRosterList(ctxA, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != "gone" {
		t.Fatalf("tombstone list = %+v", rows)
	}
	if _, err := db.AgentRosterProvision(ctxA, store.AgentRosterRow{
		ConversationID: "conv-3", Handle: "handle-a", State: "provisioning", Incarnation: "inc-4",
	}); err == nil {
		t.Fatal("permanent handle reused on real backend")
	}
}

// TestAgentRosterNoOpUpdateRecoveryDatabase forces the zero-row recovery
// branch deterministically: the retry pins the original updated_at and
// repeats the identical values, so MySQL-family drivers report zero changed
// rows even though the identity and state match.
func TestAgentRosterNoOpUpdateRecoveryDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "roster-owner-a")
	row, err := db.AgentRosterProvision(ctx, store.AgentRosterRow{
		ConversationID: "conv-1", Handle: "handle-a", TabID: "tab-a",
		State: "provisioning", Incarnation: "inc-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	noop := map[string]any{"job_id": "", "updated_at": row.UpdatedAt}
	updated, err := db.AgentRosterUpdate(ctx, row.ID, row.Identity(), noop)
	if err != nil {
		t.Fatalf("no-op update misclassified as a conflict: %v", err)
	}
	if updated.JobID != "" || updated.UpdatedAt != row.UpdatedAt {
		t.Fatalf("no-op update changed the row: %+v", updated)
	}
	cas, err := db.AgentRosterCompareAndSwapState(ctx, row.ID, row.Identity(), "provisioning", map[string]any{
		"state": "provisioning", "updated_at": row.UpdatedAt,
	})
	if err != nil {
		t.Fatalf("no-op CAS misclassified as a conflict: %v", err)
	}
	if cas.State != "provisioning" || cas.UpdatedAt != row.UpdatedAt {
		t.Fatalf("no-op CAS changed the row: %+v", cas)
	}
}
