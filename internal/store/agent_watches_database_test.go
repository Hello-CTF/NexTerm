package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// The fixture contracts below run against every supported backend through
// dbtest; `task test:database:sqlite` covers the no-service leg locally and
// the compose-backed legs cover PostgreSQL, MySQL and MariaDB.

func openAgentWatchFixture(t *testing.T) *store.Store {
	t.Helper()
	fixture := dbtest.NewFixture(t)
	backend := fixture.OpenStore(t)
	migrator := backend.GORM().Migrator()
	if !migrator.HasTable(&model.AgentWatchRow{}) || !migrator.HasTable(&model.AgentWatchOutboxRow{}) {
		t.Fatal("agent watch tables missing after startup migration")
	}
	return backend
}

func TestAgentWatchDatabaseRoundTripEveryField(t *testing.T) {
	backend := openAgentWatchFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	expires := int64(1893456000000)
	row := model.AgentWatchRow{
		ID: "watch-db-1", TargetID: "target-1", Identity: "identity-1", Name: "db watch",
		ConditionJSON: `{"type":"regex","pattern":"error \\d+"}`,
		Status:        store.AgentWatchStatusArmed, Repeat: true,
		AnchorSeq: 42, FireSeq: 0, Revision: 1,
		CreatedAt: 1000, UpdatedAt: 2000, ExpiresAt: &expires,
	}
	created, err := backend.AgentWatchCreate(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if created.OwnerID != "owner-a" {
		t.Fatalf("owner = %q", created.OwnerID)
	}
	got, err := backend.AgentWatchGet(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetID != row.TargetID || got.Identity != row.Identity || got.Name != row.Name ||
		got.ConditionJSON != row.ConditionJSON || got.Repeat != row.Repeat ||
		got.AnchorSeq != row.AnchorSeq || got.FireSeq != 0 || got.Revision != 1 ||
		got.CreatedAt != 1000 || got.UpdatedAt != 2000 ||
		got.ExpiresAt == nil || *got.ExpiresAt != expires {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, row)
	}
}

func TestAgentWatchDatabaseFireIsAtomicAndIdempotent(t *testing.T) {
	backend := openAgentWatchFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	created, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-db-fire", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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
	backend := openAgentWatchFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	created, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-db-cancel", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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

	repeat := testWatchRow("watch-db-repeat", "target-1", "identity-1")
	repeat.Repeat = true
	rearmed, err := backend.AgentWatchCreate(ctx, repeat)
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
	watch, err = backend.AgentWatchGet(ctx, rearmed.ID)
	if err != nil || watch.Status != store.AgentWatchStatusArmed || watch.AnchorSeq != 16 || watch.FireSeq != 1 {
		t.Fatalf("re-armed watch = %+v, %v", watch, err)
	}
}

func TestAgentWatchDatabaseOwnerIsolation(t *testing.T) {
	backend := openAgentWatchFixture(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	created, err := backend.AgentWatchCreate(ctxA, testWatchRow("watch-db-iso", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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
