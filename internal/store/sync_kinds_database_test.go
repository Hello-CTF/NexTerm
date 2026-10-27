package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

func syncTombstoneOf(t *testing.T, db *store.Store, ctx context.Context, id string) (store.SyncTombstone, bool) {
	t.Helper()
	tombstone := query.Use(db.GORM()).SyncTombstone
	row, err := tombstone.WithContext(ctx).Where(tombstone.ID.Eq(id), tombstone.OwnerID.Eq(ipc.OwnerID(ctx))).First()
	if err != nil {
		return store.SyncTombstone{}, false
	}
	return *row, true
}

// Synced state rule writes keep the peer revision, stay per owner, clear tombstones and respect the caller's snapshot.
func TestStateRuleSyncWritesDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "sync-rule-owner-a")
	ctxB := ipc.WithUserID(context.Background(), "sync-rule-owner-b")

	local, err := db.StateRuleSave(ctxA, stateRuleInput(`local`))
	if err != nil {
		t.Fatal(err)
	}
	input := stateRuleInput(`synced`)
	input.ID, input.UpdatedAt = ids.New(), 5000
	row, err := db.StateRuleSyncPut(ctxA, input, 4000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row.UpdatedAt != 5000 || row.CreatedAt != 4000 || row.Position <= local.Position {
		t.Fatalf("synced rule=%+v, want the peer revision after the local rule %d", row, local.Position)
	}
	if _, found, err := db.StateRuleGet(ctxB, input.ID); err != nil || found {
		t.Fatalf("another owner sees the rule: found=%v err=%v", found, err)
	}
	if _, err := db.StateRuleSyncPut(ctxA, input, 4000, nil); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("a write expecting no rule over an existing one: %v", err)
	}

	host := stateRuleInput(`host`)
	host.ID, host.Scope, host.AssetID, host.UpdatedAt = ids.New(), store.StateRuleScopeHost, ids.New(), 100
	if _, err := db.StateRuleSyncPut(ctxA, host, 100, nil); !errors.Is(err, store.ErrStateRuleAssetMissing) {
		t.Fatalf("host rule without its host: %v", err)
	}

	if applied, err := db.StateRuleSyncDelete(ctxA, input.ID, 4999); err != nil || applied {
		t.Fatalf("an older deletion applied=%v err=%v", applied, err)
	}
	if applied, err := db.StateRuleSyncDelete(ctxA, input.ID, 6000); err != nil || !applied {
		t.Fatalf("a newer deletion applied=%v err=%v", applied, err)
	}
	if tombstone, found := syncTombstoneOf(t, db, ctxA, input.ID); !found || tombstone.DeletedAt != 6000 || tombstone.Kind != store.SyncTombstoneKindStateRule {
		t.Fatalf("tombstone=%+v found=%v", tombstone, found)
	}
	input.UpdatedAt = 7000
	if _, err := db.StateRuleSyncPut(ctxA, input, 4000, nil); err != nil {
		t.Fatal(err)
	}
	if _, found := syncTombstoneOf(t, db, ctxA, input.ID); found {
		t.Fatal("a newer synced rule must clear its tombstone")
	}

	if err := db.StateRuleDelete(ctxA, local.ID); err != nil {
		t.Fatal(err)
	}
	if _, found := syncTombstoneOf(t, db, ctxA, local.ID); !found {
		t.Fatal("a user deletion must leave a tombstone for the owner's other devices")
	}
}

// Synced runbook templates keep the peer version and revision, stay per owner, and deletions leave tombstones.
func TestRunbookTemplateSyncWritesDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "sync-runbook-owner-a")
	ctxB := ipc.WithUserID(context.Background(), "sync-runbook-owner-b")

	template := store.RunbookTemplate{
		ID: ids.New(), Name: "deploy", Description: "", Version: 4, CreatedAt: 100, UpdatedAt: 300,
		Steps: []store.RunbookTemplateStep{{ID: "s1", SnippetID: ids.New(), Name: "pull", Command: "git pull"}},
	}
	if err := db.RunbookTemplateSyncPut(ctxA, template, nil); err != nil {
		t.Fatal(err)
	}
	got, err := db.RunbookTemplateGet(ctxA, template.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 4 || got.UpdatedAt != 300 || got.Steps[0].SnippetID != template.Steps[0].SnippetID {
		t.Fatalf("synced template=%+v", got)
	}
	if _, err := db.RunbookTemplateGet(ctxB, template.ID); err == nil {
		t.Fatal("another owner sees the template")
	}
	stale := got
	stale.UpdatedAt = 1
	next := template
	next.Name, next.UpdatedAt, next.Version = "deploy v2", 400, 5
	if err := db.RunbookTemplateSyncPut(ctxA, next, &stale); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("a write bound to a stale snapshot: %v", err)
	}
	if err := db.RunbookTemplateSyncPut(ctxA, next, &got); err != nil {
		t.Fatal(err)
	}
	invalid := next
	invalid.ID, invalid.Steps = ids.New(), []store.RunbookTemplateStep{{ID: "s1", Name: "empty"}}
	if err := db.RunbookTemplateSyncPut(ctxA, invalid, nil); err == nil {
		t.Fatal("a step without a command must be rejected")
	}

	if applied, err := db.RunbookTemplateSyncDelete(ctxA, template.ID, 350); err != nil || applied {
		t.Fatalf("an older deletion applied=%v err=%v", applied, err)
	}
	if applied, err := db.RunbookTemplateSyncDelete(ctxA, template.ID, 500); err != nil || !applied {
		t.Fatalf("a newer deletion applied=%v err=%v", applied, err)
	}
	if _, err := db.RunbookTemplateGet(ctxA, template.ID); err == nil {
		t.Fatal("the deleted template remains")
	}

	created, err := db.RunbookTemplateCreate(ctxB, store.RunbookTemplateInput{Name: "local", Steps: []store.RunbookTemplateStep{{ID: "s1", Name: "x", Command: "uptime"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunbookTemplateDelete(ctxB, created.ID); err != nil {
		t.Fatal(err)
	}
	if tombstone, found := syncTombstoneOf(t, db, ctxB, created.ID); !found || tombstone.Kind != store.SyncTombstoneKindRunbookTemplate {
		t.Fatalf("user deletion tombstone=%+v found=%v", tombstone, found)
	}
}
