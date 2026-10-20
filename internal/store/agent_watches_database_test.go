package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// The fixture contracts below run against every supported backend through
// dbtest; `task test:database:sqlite` covers the no-service leg locally and
// the compose-backed legs cover PostgreSQL, MySQL and MariaDB.

func openAgentWatchFixture(t *testing.T) (*dbtest.Fixture, *store.Store) {
	t.Helper()
	fixture := dbtest.NewFixture(t)
	backend := fixture.OpenStore(t)
	migrator := backend.GORM().Migrator()
	if !migrator.HasTable(&model.AgentWatchRow{}) || !migrator.HasTable(&model.AgentWatchOutboxRow{}) || !migrator.HasTable(&model.AgentWatchTargetRow{}) {
		t.Fatal("agent watch tables missing after startup migration")
	}
	return fixture, backend
}

func reopenAgentWatchStore(t *testing.T, fixture *dbtest.Fixture) *store.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	backend, err := store.OpenBackend(ctx, store.BackendConfig{Backend: fixture.Backend, DSN: fixture.DSN}, store.OpenOptions{})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	return backend
}

func TestAgentWatchDatabaseRoundTripEveryField(t *testing.T) {
	_, backend := openAgentWatchFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	attachWatchTarget(t, backend, ctx, "target-1", "identity-1")
	expires := int64(1893456000000)
	row := model.AgentWatchRow{
		ID: "watch-db-1", TargetID: "target-1", Name: "db watch",
		ConditionJSON: `{"type":"regex","pattern":"error \\d+"}`,
		Repeat:        true, AnchorSeq: 42,
		CarryBlob: []byte("partial"),
		CreatedAt: 1000, UpdatedAt: 2000, ExpiresAt: &expires,
	}
	created, err := backend.AgentWatchCreate(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if created.OwnerID != "owner-a" || created.Identity != "identity-1" || created.Generation != 1 {
		t.Fatalf("created binding = %+v", created)
	}
	got, err := backend.AgentWatchGet(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetID != row.TargetID || got.Name != row.Name ||
		got.ConditionJSON != row.ConditionJSON || got.Repeat != row.Repeat ||
		got.AnchorSeq != row.AnchorSeq || string(got.CarryBlob) != "partial" ||
		got.FireSeq != 0 || got.Revision != 1 ||
		got.CreatedAt != 1000 || got.UpdatedAt != 2000 ||
		got.ExpiresAt == nil || *got.ExpiresAt != expires {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, row)
	}
}

func TestAgentWatchDatabaseFireIsAtomicAndIdempotent(t *testing.T) {
	_, backend := openAgentWatchFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	created := createTestWatch(t, backend, ctx, "watch-db-fire", "target-1", "identity-1")
	event, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-db-1", 64))
	if err != nil {
		t.Fatal(err)
	}
	watch, err := backend.AgentWatchGet(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if watch.Status != store.AgentWatchStatusMatched || watch.FireSeq != 1 || watch.Revision != 2 {
		t.Fatalf("watch after fire = %+v", watch)
	}
	stored, err := backend.AgentWatchEventGet(ctx, event.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != store.AgentWatchEventPending || stored.WatchID != created.ID ||
		stored.FireSeq != 1 || stored.EventJSON != event.EventJSON || stored.Kind != "watchEvent" {
		t.Fatalf("stored event = %+v", stored)
	}
	// Retrying the same logical fire with the fixed IDs returns the recorded
	// event without a second row.
	retry, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-db-1", 64))
	if err != nil {
		t.Fatalf("idempotent fire retry: %v", err)
	}
	if retry.DeliveryID != event.DeliveryID || retry.RunID != event.RunID || retry.CreatedAt != event.CreatedAt {
		t.Fatalf("retry = %+v; want %+v", retry, event)
	}
	// A fresh delivery ID for the same fire is rejected on every backend.
	if _, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-db-2", 64)); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("double fire = %v; want conflict", err)
	}
	pending, err := backend.AgentWatchEventPendingList(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, %v; want exactly one", pending, err)
	}
}

func TestAgentWatchDatabaseCancelAndAckAreAtomic(t *testing.T) {
	_, backend := openAgentWatchFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	created := createTestWatch(t, backend, ctx, "watch-db-cancel", "target-1", "identity-1")
	if _, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-db-c", 8)); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchCancel(ctx, created.ID, 2, 3000); err != nil {
		t.Fatal(err)
	}
	watch, err := backend.AgentWatchGet(ctx, created.ID)
	if err != nil || watch.Status != store.AgentWatchStatusCancelled {
		t.Fatalf("watch after cancel = %+v, %v", watch, err)
	}
	event, err := backend.AgentWatchEventGet(ctx, "delivery-db-c")
	if err != nil || event.Status != store.AgentWatchEventCancelled {
		t.Fatalf("event after cancel = %+v, %v", event, err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, "delivery-db-c", 2, 4000); !errors.Is(err, store.ErrAgentWatchState) {
		t.Fatalf("ack cancelled event = %v; want state error", err)
	}

	repeat := testWatchRow("watch-db-repeat", "target-1")
	repeat.Repeat = true
	if _, err := backend.AgentWatchCreate(ctx, repeat); err != nil {
		t.Fatal(err)
	}
	rearmed, err := backend.AgentWatchGet(ctx, "watch-db-repeat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchFire(ctx, fireParams(rearmed, "delivery-db-r", 16)); err != nil {
		t.Fatal(err)
	}
	acked, err := backend.AgentWatchEventAck(ctx, "delivery-db-r", 1, 4000)
	if err != nil || acked.Status != store.AgentWatchEventAcked {
		t.Fatalf("ack = %+v, %v", acked, err)
	}
	watch, err = backend.AgentWatchGet(ctx, "watch-db-repeat")
	if err != nil || watch.Status != store.AgentWatchStatusArmed || watch.AnchorSeq != 16 || watch.FireSeq != 1 {
		t.Fatalf("re-armed watch = %+v, %v", watch, err)
	}
}

func TestAgentWatchDatabaseOwnerIsolation(t *testing.T) {
	_, backend := openAgentWatchFixture(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	created := createTestWatch(t, backend, ctxA, "watch-db-iso", "target-1", "identity-1")
	if _, err := backend.AgentWatchFire(ctxA, fireParams(created, "delivery-db-iso", 4)); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchGet(ctxB, created.ID); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("cross-owner get = %v; want not found", err)
	}
	if _, err := backend.AgentWatchCancel(ctxB, created.ID, 1, 3000); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("cross-owner cancel = %v; want not found", err)
	}
	if _, err := backend.AgentWatchEventGet(ctxB, "delivery-db-iso"); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("cross-owner event get = %v; want not found", err)
	}
	pending, err := backend.AgentWatchEventPendingList(ctxB, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("cross-owner pending = %+v, %v; want empty", pending, err)
	}
	count, err := backend.AgentWatchExpireDue(ctxB, 9999999999999)
	if err != nil || count != 0 {
		t.Fatalf("cross-owner expire = %d, %v; want 0", count, err)
	}
	// Owner A's rows are untouched by B's attempts.
	watch, err := backend.AgentWatchGet(ctxA, created.ID)
	if err != nil || watch.Status != store.AgentWatchStatusMatched {
		t.Fatalf("owner-a watch = %+v, %v", watch, err)
	}
}

func TestAgentWatchDatabaseTargetDurabilityAcrossRestart(t *testing.T) {
	fixture, backend := openAgentWatchFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	attachWatchTarget(t, backend, ctx, "target-db", "identity-1")
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-db-restart", "target-db")); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen on the same database: the durable target row must still
	// distinguish a valid permanent detach from a stale one.
	backend = reopenAgentWatchStore(t, fixture)
	defer backend.Close()
	row, err := backend.AgentWatchTargetGet(ctx, "target-db")
	if err != nil || row.Identity != "identity-1" || row.Generation != 1 || row.Gone {
		t.Fatalf("target after restart = %+v, %v", row, err)
	}
	if settled, err := backend.AgentWatchTargetDetach(ctx, "target-db", "identity-stale", 2000); err != nil || settled {
		t.Fatalf("stale detach after restart = %v, %v", settled, err)
	}
	if watch, err := backend.AgentWatchGet(ctx, "watch-db-restart"); err != nil || watch.Status != store.AgentWatchStatusArmed {
		t.Fatalf("watch after stale detach = %+v, %v", watch, err)
	}
	settled, err := backend.AgentWatchTargetDetach(ctx, "target-db", "identity-1", 3000)
	if err != nil || !settled {
		t.Fatalf("permanent detach after restart = %v, %v", settled, err)
	}
	watch, err := backend.AgentWatchGet(ctx, "watch-db-restart")
	if err != nil || watch.Status != store.AgentWatchStatusUnverifiable {
		t.Fatalf("watch after permanent detach = %+v, %v", watch, err)
	}
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-db-gone", "target-db")); !errors.Is(err, store.ErrAgentWatchDetached) {
		t.Fatalf("create after permanent detach = %v; want detached", err)
	}
}
