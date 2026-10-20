package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

func openAgentWatchStore(t *testing.T) *store.Store {
	t.Helper()
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

func watchOwnerCtx(owner string) context.Context {
	return ipc.WithUserID(context.Background(), owner)
}

func attachWatchTarget(t *testing.T, backend *store.Store, ctx context.Context, targetID, identity string) model.AgentWatchTargetRow {
	t.Helper()
	row, err := backend.AgentWatchTargetAttach(ctx, targetID, identity, 1000)
	if err != nil {
		t.Fatalf("attach target %s: %v", targetID, err)
	}
	return row
}

func testWatchRow(id, target string) model.AgentWatchRow {
	return model.AgentWatchRow{
		ID: id, TargetID: target, Name: "watch-" + id,
		ConditionJSON: `{"type":"contains","pattern":"ready"}`,
		CreatedAt:     1000, UpdatedAt: 1000,
	}
}

func createTestWatch(t *testing.T, backend *store.Store, ctx context.Context, id, target, identity string) model.AgentWatchRow {
	t.Helper()
	attachWatchTarget(t, backend, ctx, target, identity)
	row, err := backend.AgentWatchCreate(ctx, testWatchRow(id, target))
	if err != nil {
		t.Fatalf("create watch %s: %v", id, err)
	}
	return row
}

func TestAgentWatchTablesMigratedAtStartup(t *testing.T) {
	backend := openAgentWatchStore(t)
	migrator := backend.GORM().Migrator()
	for _, table := range []any{&model.AgentWatchRow{}, &model.AgentWatchOutboxRow{}, &model.AgentWatchTargetRow{}} {
		if !migrator.HasTable(table) {
			t.Fatalf("%T table missing after startup migration", table)
		}
	}
	ctx := watchOwnerCtx("owner-a")
	attachWatchTarget(t, backend, ctx, "target-1", "identity-1")
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-idem", "target-1")); err != nil {
		t.Fatal(err)
	}
	got, err := backend.AgentWatchGet(ctx, "watch-idem")
	if err != nil || got.TargetID != "target-1" {
		t.Fatalf("watch after startup migration = %+v, %v", got, err)
	}
}

func TestAgentWatchCreateBindsTargetIdentityAndGeneration(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	attachWatchTarget(t, backend, ctx, "target-1", "identity-1")
	row := testWatchRow("watch-create-1", "target-1")
	row.OwnerID = "owner-evil"
	row.Identity = "identity-evil"
	row.Generation = 42
	row.Status = store.AgentWatchStatusMatched
	row.Revision = 99
	created, err := backend.AgentWatchCreate(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if created.OwnerID != "owner-a" {
		t.Fatalf("owner = %q; want owner-a from context", created.OwnerID)
	}
	if created.Identity != "identity-1" || created.Generation != 1 {
		t.Fatalf("binding = identity %q generation %d; want identity-1/1", created.Identity, created.Generation)
	}
	if created.Status != store.AgentWatchStatusArmed || created.Revision != 1 {
		t.Fatalf("created = %+v; want armed revision 1", created)
	}
}

func TestAgentWatchCreateValidation(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	attachWatchTarget(t, backend, ctx, "target-1", "identity-1")
	cases := map[string]model.AgentWatchRow{
		"empty id":      {TargetID: "target-1", ConditionJSON: `{}`},
		"empty target":  {ID: "watch-bad-target", ConditionJSON: `{}`},
		"bad condition": {ID: "watch-bad-cond", TargetID: "target-1", ConditionJSON: `{`},
	}
	for name, row := range cases {
		if _, err := backend.AgentWatchCreate(ctx, row); err == nil {
			t.Fatalf("%s: create succeeded", name)
		}
	}
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-dup", "target-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-dup", "target-1")); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("duplicate create = %v; want ErrAgentWatchConflict", err)
	}
}

func TestAgentWatchCreateRequiresAttachedTarget(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-no-target", "target-missing")); !errors.Is(err, store.ErrAgentWatchDetached) {
		t.Fatalf("create without target = %v; want ErrAgentWatchDetached", err)
	}
	attachWatchTarget(t, backend, ctx, "target-1", "identity-1")
	if _, err := backend.AgentWatchTargetDetach(ctx, "target-1", "identity-1", 2000); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-gone", "target-1")); !errors.Is(err, store.ErrAgentWatchDetached) {
		t.Fatalf("create after permanent detach = %v; want ErrAgentWatchDetached", err)
	}
}

func TestAgentWatchOwnerIsolation(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctxA := watchOwnerCtx("owner-a")
	ctxB := watchOwnerCtx("owner-b")
	created := createTestWatch(t, backend, ctxA, "watch-iso", "target-iso", "identity-1")
	if _, err := backend.AgentWatchGet(ctxB, created.ID); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("cross-owner get = %v; want ErrAgentWatchNotFound", err)
	}
	rows, err := backend.AgentWatchListByTarget(ctxB, "target-iso")
	if err != nil || len(rows) != 0 {
		t.Fatalf("cross-owner list = %+v, %v; want empty", rows, err)
	}
	if _, err := backend.AgentWatchAdvance(ctxB, created.ID, created.Revision, "identity-1", 10, nil, 2000); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("cross-owner advance = %v; want ErrAgentWatchNotFound", err)
	}
	if _, err := backend.AgentWatchFire(ctxB, store.AgentWatchFireParams{
		WatchID: created.ID, ExpectedRevision: created.Revision, Identity: "identity-1",
		DeliveryID: "delivery-b", RunID: "run-b", AnchorSeq: 10, EventKind: "watchEvent", EventJSON: `{}`, Now: 2000,
	}); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("cross-owner fire = %v; want ErrAgentWatchNotFound", err)
	}
	if _, err := backend.AgentWatchCancel(ctxB, created.ID, created.Revision, 2000); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("cross-owner cancel = %v; want ErrAgentWatchNotFound", err)
	}
}

func fireParams(watch model.AgentWatchRow, deliveryID string, anchor int64) store.AgentWatchFireParams {
	return store.AgentWatchFireParams{
		WatchID: watch.ID, ExpectedRevision: watch.Revision, Identity: watch.Identity,
		DeliveryID: deliveryID, RunID: "run-" + deliveryID,
		AnchorSeq: anchor, MatchedFrom: 2, MatchedTo: 6, Excerpt: "ready",
		EventKind: "watchEvent", EventJSON: `{"kind":"watchEvent"}`,
		Now: 2000,
	}
}

func TestAgentWatchAdvanceGuards(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	created := createTestWatch(t, backend, ctx, "watch-adv", "target-1", "identity-1")
	advanced, err := backend.AgentWatchAdvance(ctx, created.ID, created.Revision, "identity-1", 10, []byte("tail"), 2000)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.AnchorSeq != 10 || advanced.Revision != 2 || string(advanced.CarryBlob) != "tail" {
		t.Fatalf("advanced = %+v", advanced)
	}
	if _, err := backend.AgentWatchAdvance(ctx, created.ID, created.Revision, "identity-1", 20, nil, 2000); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("stale revision advance = %v; want conflict", err)
	}
	if _, err := backend.AgentWatchAdvance(ctx, advanced.ID, advanced.Revision, "identity-1", 5, nil, 2000); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("anchor regression = %v; want conflict", err)
	}
	if _, err := backend.AgentWatchAdvance(ctx, advanced.ID, advanced.Revision, "identity-2", 20, nil, 2000); !errors.Is(err, store.ErrAgentWatchIdentity) {
		t.Fatalf("identity mismatch = %v; want ErrAgentWatchIdentity", err)
	}
	if _, err := backend.AgentWatchAdvance(ctx, "missing", 1, "identity-1", 20, nil, 2000); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("missing advance = %v; want not found", err)
	}
}

func TestAgentWatchAdvanceCarryBound(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	created := createTestWatch(t, backend, ctx, "watch-carry-bound", "target-1", "identity-1")
	if _, err := backend.AgentWatchAdvance(ctx, created.ID, created.Revision, "identity-1", 10, make([]byte, store.AgentWatchMaxCarryBytes+1), 2000); err == nil {
		t.Fatal("advance with oversized carry succeeded")
	}
	advanced, err := backend.AgentWatchAdvance(ctx, created.ID, created.Revision, "identity-1", 10, make([]byte, store.AgentWatchMaxCarryBytes), 2000)
	if err != nil || len(advanced.CarryBlob) != store.AgentWatchMaxCarryBytes {
		t.Fatalf("advance with hard-bound carry = %+v, %v", advanced, err)
	}
}

func TestAgentWatchConsumeGuards(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	armed := createTestWatch(t, backend, ctx, "watch-consume-armed", "target-1", "identity-1")
	if _, err := backend.AgentWatchConsume(ctx, armed.ID, armed.Revision, "identity-1", 10, 2000); !errors.Is(err, store.ErrAgentWatchState) {
		t.Fatalf("consume armed = %v; want state error", err)
	}
	oneShot := createTestWatch(t, backend, ctx, "watch-consume-oneshot", "target-1", "identity-1")
	if _, err := backend.AgentWatchFire(ctx, fireParams(oneShot, "delivery-consume-1", 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchConsume(ctx, oneShot.ID, 2, "identity-1", 20, 2000); !errors.Is(err, store.ErrAgentWatchState) {
		t.Fatalf("consume one-shot matched = %v; want state error", err)
	}
	repeat := testWatchRow("watch-consume-repeat", "target-1")
	repeat.Repeat = true
	repeatRow, err := backend.AgentWatchCreate(ctx, repeat)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchAdvance(ctx, repeatRow.ID, repeatRow.Revision, "identity-1", 10, []byte("tail"), 2000); err != nil {
		t.Fatal(err)
	}
	fire := fireParams(repeatRow, "delivery-consume-2", 10)
	fire.ExpectedRevision = 2
	if _, err := backend.AgentWatchFire(ctx, fire); err != nil {
		t.Fatal(err)
	}
	consumed, err := backend.AgentWatchConsume(ctx, repeatRow.ID, 3, "identity-1", 20, 2000)
	if err != nil || consumed.AnchorSeq != 20 || len(consumed.CarryBlob) != 0 {
		t.Fatalf("consumed = %+v, %v", consumed, err)
	}
	if _, err := backend.AgentWatchConsume(ctx, repeatRow.ID, 3, "identity-1", 30, 2000); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("stale consume = %v; want conflict", err)
	}
	if _, err := backend.AgentWatchConsume(ctx, repeatRow.ID, 4, "identity-2", 30, 2000); !errors.Is(err, store.ErrAgentWatchIdentity) {
		t.Fatalf("identity consume = %v; want identity error", err)
	}
}

func TestAgentWatchFireWritesWatchAndOutboxTogether(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	created := createTestWatch(t, backend, ctx, "watch-fire", "target-1", "identity-1")
	if _, err := backend.AgentWatchAdvance(ctx, created.ID, created.Revision, "identity-1", 8, []byte("tail"), 2000); err != nil {
		t.Fatal(err)
	}
	created.Revision = 2
	event, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-1", 10))
	if err != nil {
		t.Fatal(err)
	}
	if event.Status != store.AgentWatchEventPending || event.FireSeq != 1 || event.Revision != 1 {
		t.Fatalf("event = %+v", event)
	}
	watch, err := backend.AgentWatchGet(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if watch.Status != store.AgentWatchStatusMatched || watch.AnchorSeq != 10 || watch.FireSeq != 1 ||
		watch.DeliveryID != "delivery-1" || watch.RunID != "run-delivery-1" || watch.Revision != 3 ||
		len(watch.CarryBlob) != 0 {
		t.Fatalf("watch after fire = %+v", watch)
	}
	// Same logical fire retried with identical fixed IDs is idempotent.
	retry := fireParams(created, "delivery-1", 10)
	retry.ExpectedRevision = 2
	again, err := backend.AgentWatchFire(ctx, retry)
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if again.DeliveryID != event.DeliveryID || again.RunID != event.RunID || again.CreatedAt != event.CreatedAt {
		t.Fatalf("retry = %+v; want original %+v", again, event)
	}
	// A double fire with a fresh delivery ID for the same revision is rejected.
	if _, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-2", 10)); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("double fire = %v; want conflict", err)
	}
	// DeliveryID reuse across watches is rejected.
	other := createTestWatch(t, backend, ctx, "watch-fire-2", "target-1", "identity-1")
	if _, err := backend.AgentWatchFire(ctx, fireParams(other, "delivery-1", 10)); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("delivery reuse = %v; want conflict", err)
	}
}

func TestAgentWatchFireGuards(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	created := createTestWatch(t, backend, ctx, "watch-guard", "target-1", "identity-1")
	stale := fireParams(created, "delivery-s", 10)
	stale.ExpectedRevision = 99
	if _, err := backend.AgentWatchFire(ctx, stale); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("stale fire = %v; want conflict", err)
	}
	wrongIdentity := fireParams(created, "delivery-i", 10)
	wrongIdentity.Identity = "identity-2"
	if _, err := backend.AgentWatchFire(ctx, wrongIdentity); !errors.Is(err, store.ErrAgentWatchIdentity) {
		t.Fatalf("identity fire = %v; want ErrAgentWatchIdentity", err)
	}
	if _, err := backend.AgentWatchAdvance(ctx, created.ID, created.Revision, "identity-1", 10, nil, 2000); err != nil {
		t.Fatal(err)
	}
	regression := fireParams(created, "delivery-r", 10)
	regression.ExpectedRevision = 2
	regression.AnchorSeq = 5
	if _, err := backend.AgentWatchFire(ctx, regression); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("anchor regression fire = %v; want conflict", err)
	}
	missing := fireParams(created, "delivery-m", 10)
	missing.WatchID = "missing"
	if _, err := backend.AgentWatchFire(ctx, missing); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("missing fire = %v; want not found", err)
	}
	noIDs := fireParams(created, "", 10)
	if _, err := backend.AgentWatchFire(ctx, noIDs); err == nil {
		t.Fatal("fire without fixed delivery/run IDs succeeded")
	}
}

func TestAgentWatchFireBackstopRejectsSecondFireSeqRow(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	created := createTestWatch(t, backend, ctx, "watch-backstop", "target-1", "identity-1")
	if _, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-b1", 10)); err != nil {
		t.Fatal(err)
	}
	// Fault injection: force the watch back to armed with the original fire
	// sequence, simulating a writer that tries to record the same fire
	// sequence twice with a fresh delivery ID.
	if err := backend.GORM().Model(&model.AgentWatchRow{}).Where("id = ?", created.ID).
		Updates(map[string]any{"status": store.AgentWatchStatusArmed, "fire_seq": 0, "delivery_id": "", "revision": 3}).Error; err != nil {
		t.Fatal(err)
	}
	retry := fireParams(created, "delivery-b2", 10)
	retry.ExpectedRevision = 3
	if _, err := backend.AgentWatchFire(ctx, retry); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("fire_seq backstop = %v; want conflict", err)
	}
}

func TestAgentWatchCancelSettlesPendingEventAtomically(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	created := createTestWatch(t, backend, ctx, "watch-cancel", "target-1", "identity-1")
	if _, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-c1", 10)); err != nil {
		t.Fatal(err)
	}
	cancelled, err := backend.AgentWatchCancel(ctx, created.ID, 2, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != store.AgentWatchStatusCancelled || cancelled.Revision != 3 {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	event, err := backend.AgentWatchEventGet(ctx, "delivery-c1")
	if err != nil {
		t.Fatal(err)
	}
	if event.Status != store.AgentWatchEventCancelled {
		t.Fatalf("pending event after cancel = %s; want cancelled", event.Status)
	}
	pending, err := backend.AgentWatchEventPendingList(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after cancel = %+v, %v; want empty", pending, err)
	}
	// Cancelling again is an idempotent no-op even with a stale revision.
	again, err := backend.AgentWatchCancel(ctx, created.ID, 2, 3000)
	if err != nil || again.Revision != cancelled.Revision {
		t.Fatalf("idempotent cancel = %+v, %v", again, err)
	}
	if _, err := backend.AgentWatchCancel(ctx, "missing", 1, 3000); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("missing cancel = %v; want not found", err)
	}
}

func TestAgentWatchEventAckAndRepeatReArm(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	repeat := testWatchRow("watch-repeat", "target-1")
	repeat.Repeat = true
	attachWatchTarget(t, backend, ctx, "target-1", "identity-1")
	created, err := backend.AgentWatchCreate(ctx, repeat)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-r1", 10)); err != nil {
		t.Fatal(err)
	}
	acked, err := backend.AgentWatchEventAck(ctx, "delivery-r1", 1, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if acked.Status != store.AgentWatchEventAcked || acked.DeliveredAt == nil || *acked.DeliveredAt != 4000 {
		t.Fatalf("acked = %+v", acked)
	}
	watch, err := backend.AgentWatchGet(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if watch.Status != store.AgentWatchStatusArmed || watch.AnchorSeq != 10 || watch.FireSeq != 1 ||
		watch.DeliveryID != "" || watch.RunID != "" || watch.Excerpt != "" {
		t.Fatalf("re-armed watch = %+v", watch)
	}
	// Duplicate ack with a stale revision is an idempotent no-op.
	again, err := backend.AgentWatchEventAck(ctx, "delivery-r1", 1, 4000)
	if err != nil || again.Revision != acked.Revision {
		t.Fatalf("idempotent ack = %+v, %v", again, err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, "delivery-r1", 99, 4000); err != nil {
		// The row is already acked, so any retry stays a no-op.
		t.Fatalf("stale ack on acked row = %v; want idempotent success", err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, "missing", 1, 4000); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("missing ack = %v; want not found", err)
	}
}

func TestAgentWatchEventAckRejectsCancelledAndStale(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	created := createTestWatch(t, backend, ctx, "watch-ack", "target-1", "identity-1")
	if _, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-a1", 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, "delivery-a1", 99, 4000); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("stale ack = %v; want conflict", err)
	}
	if _, err := backend.AgentWatchCancel(ctx, created.ID, 2, 3000); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, "delivery-a1", 2, 4000); !errors.Is(err, store.ErrAgentWatchState) {
		t.Fatalf("ack cancelled = %v; want state error", err)
	}
	// A non-repeat watch stays matched after ack.
	oneShot := createTestWatch(t, backend, ctx, "watch-oneshot", "target-1", "identity-1")
	if _, err := backend.AgentWatchFire(ctx, fireParams(oneShot, "delivery-o1", 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, "delivery-o1", 1, 4000); err != nil {
		t.Fatal(err)
	}
	watch, err := backend.AgentWatchGet(ctx, oneShot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if watch.Status != store.AgentWatchStatusMatched {
		t.Fatalf("one-shot watch after ack = %s; want matched", watch.Status)
	}
}

func TestAgentWatchEventPendingListOwnerScopedAndOrdered(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctxA := watchOwnerCtx("owner-a")
	ctxB := watchOwnerCtx("owner-b")
	attachWatchTarget(t, backend, ctxA, "target-1", "identity-1")
	for index, id := range []string{"watch-p1", "watch-p2"} {
		row := testWatchRow(id, "target-1")
		row.CreatedAt = int64(1000 + index)
		created, err := backend.AgentWatchCreate(ctxA, row)
		if err != nil {
			t.Fatal(err)
		}
		params := fireParams(created, fmt.Sprintf("delivery-p%d", index+1), 10)
		params.Now = int64(2000 + index)
		if _, err := backend.AgentWatchFire(ctxA, params); err != nil {
			t.Fatal(err)
		}
	}
	other := createTestWatch(t, backend, ctxB, "watch-p3", "target-1", "identity-1")
	if _, err := backend.AgentWatchFire(ctxB, fireParams(other, "delivery-p3", 10)); err != nil {
		t.Fatal(err)
	}
	pending, err := backend.AgentWatchEventPendingList(ctxA, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || pending[0].DeliveryID != "delivery-p1" || pending[1].DeliveryID != "delivery-p2" {
		t.Fatalf("pending = %+v", pending)
	}
	limited, err := backend.AgentWatchEventPendingList(ctxA, 1)
	if err != nil || len(limited) != 1 || limited[0].DeliveryID != "delivery-p1" {
		t.Fatalf("limited = %+v, %v", limited, err)
	}
	pendingB, err := backend.AgentWatchEventPendingList(ctxB, 10)
	if err != nil || len(pendingB) != 1 || pendingB[0].DeliveryID != "delivery-p3" {
		t.Fatalf("pending B = %+v, %v", pendingB, err)
	}
	if _, err := backend.AgentWatchEventGet(ctxB, "delivery-p1"); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("cross-owner event get = %v; want not found", err)
	}
}

func TestAgentWatchMarkUnverifiable(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	created := createTestWatch(t, backend, ctx, "watch-unver", "target-1", "identity-1")
	marked, err := backend.AgentWatchMarkUnverifiable(ctx, created.ID, created.Revision, "output gap", 2000)
	if err != nil {
		t.Fatal(err)
	}
	if marked.Status != store.AgentWatchStatusUnverifiable || marked.LastError != "output gap" {
		t.Fatalf("marked = %+v", marked)
	}
	// Idempotent when already unverifiable, strict for other terminal states.
	again, err := backend.AgentWatchMarkUnverifiable(ctx, created.ID, 1, "output gap", 2000)
	if err != nil || again.Revision != marked.Revision {
		t.Fatalf("idempotent mark = %+v, %v", again, err)
	}
	armed := createTestWatch(t, backend, ctx, "watch-unver-2", "target-1", "identity-1")
	if _, err := backend.AgentWatchCancel(ctx, armed.ID, armed.Revision, 2000); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchMarkUnverifiable(ctx, armed.ID, 2, "late", 2000); !errors.Is(err, store.ErrAgentWatchState) {
		t.Fatalf("mark cancelled = %v; want state error", err)
	}
	if _, err := backend.AgentWatchMarkUnverifiable(ctx, armed.ID, 1, "late", 2000); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("stale mark = %v; want conflict", err)
	}
}

func TestAgentWatchExpireDue(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	attachWatchTarget(t, backend, ctx, "target-1", "identity-1")
	expired := testWatchRow("watch-exp-1", "target-1")
	past := int64(1000)
	expired.ExpiresAt = &past
	if _, err := backend.AgentWatchCreate(ctx, expired); err != nil {
		t.Fatal(err)
	}
	future := int64(9999999999)
	later := testWatchRow("watch-exp-2", "target-1")
	later.ExpiresAt = &future
	if _, err := backend.AgentWatchCreate(ctx, later); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-exp-3", "target-1")); err != nil {
		t.Fatal(err)
	}
	attachWatchTarget(t, backend, watchOwnerCtx("owner-b"), "target-1", "identity-1")
	otherOwner := testWatchRow("watch-exp-4", "target-1")
	otherOwner.ExpiresAt = &past
	if _, err := backend.AgentWatchCreate(watchOwnerCtx("owner-b"), otherOwner); err != nil {
		t.Fatal(err)
	}
	count, err := backend.AgentWatchExpireDue(ctx, 5000)
	if err != nil || count != 1 {
		t.Fatalf("expire due = %d, %v; want 1", count, err)
	}
	watch, err := backend.AgentWatchGet(ctx, "watch-exp-1")
	if err != nil || watch.Status != store.AgentWatchStatusExpired {
		t.Fatalf("expired watch = %+v, %v", watch, err)
	}
	for _, id := range []string{"watch-exp-2", "watch-exp-3"} {
		watch, err := backend.AgentWatchGet(ctx, id)
		if err != nil || watch.Status != store.AgentWatchStatusArmed {
			t.Fatalf("watch %s = %+v, %v; want armed", id, watch, err)
		}
	}
	if watch, err := backend.AgentWatchGet(watchOwnerCtx("owner-b"), "watch-exp-4"); err != nil || watch.Status != store.AgentWatchStatusArmed {
		t.Fatalf("owner-b watch = %+v, %v; want armed", watch, err)
	}
}

func TestAgentWatchFireRequiresArmedAndValidEvent(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	created := createTestWatch(t, backend, ctx, "watch-json", "target-1", "identity-1")
	badJSON := fireParams(created, "delivery-j", 10)
	badJSON.EventJSON = `{`
	if _, err := backend.AgentWatchFire(ctx, badJSON); err == nil {
		t.Fatal("fire with invalid event JSON succeeded")
	}
	if _, err := backend.AgentWatchCancel(ctx, created.ID, created.Revision, 2000); err != nil {
		t.Fatal(err)
	}
	cancelledFire := fireParams(created, "delivery-j", 10)
	cancelledFire.ExpectedRevision = 2
	if _, err := backend.AgentWatchFire(ctx, cancelledFire); !errors.Is(err, store.ErrAgentWatchState) {
		t.Fatalf("fire cancelled = %v; want state error", err)
	}
}

func TestAgentWatchTimestampsSurvive(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	attachWatchTarget(t, backend, ctx, "target-1", "identity-1")
	expires := time.Date(2026, time.March, 1, 2, 3, 4, 0, time.UTC).UnixMilli()
	row := testWatchRow("watch-ts", "target-1")
	row.CreatedAt = 1111
	row.UpdatedAt = 2222
	row.ExpiresAt = &expires
	created, err := backend.AgentWatchCreate(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if created.CreatedAt != 1111 || created.UpdatedAt != 2222 || created.ExpiresAt == nil || *created.ExpiresAt != expires {
		t.Fatalf("timestamps = %+v", created)
	}
}

func TestAgentWatchTargetLifecycle(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	row, err := backend.AgentWatchTargetAttach(ctx, "target-1", "identity-1", 1000)
	if err != nil || row.Generation != 1 || row.Gone {
		t.Fatalf("first attach = %+v, %v", row, err)
	}
	// Same-identity attach is idempotent.
	again, err := backend.AgentWatchTargetAttach(ctx, "target-1", "identity-1", 2000)
	if err != nil || again.Generation != 1 {
		t.Fatalf("idempotent attach = %+v, %v", again, err)
	}
	armed := createTestWatch(t, backend, ctx, "watch-target-1", "target-1", "identity-1")
	// A stale detach carrying another identity is a no-op.
	settled, err := backend.AgentWatchTargetDetach(ctx, "target-1", "identity-2", 3000)
	if err != nil || settled {
		t.Fatalf("stale detach = %v, %v", settled, err)
	}
	if watch, err := backend.AgentWatchGet(ctx, armed.ID); err != nil || watch.Status != store.AgentWatchStatusArmed {
		t.Fatalf("watch after stale detach = %+v, %v", watch, err)
	}
	// Identity replacement settles the old incarnation and bumps generation.
	replaced, err := backend.AgentWatchTargetAttach(ctx, "target-1", "identity-2", 4000)
	if err != nil || replaced.Generation != 2 || replaced.Identity != "identity-2" {
		t.Fatalf("replacement attach = %+v, %v", replaced, err)
	}
	if watch, err := backend.AgentWatchGet(ctx, armed.ID); err != nil || watch.Status != store.AgentWatchStatusUnverifiable {
		t.Fatalf("watch after replacement = %+v, %v", watch, err)
	}
	// Permanent detach is durable: gone + generation bump + settlement.
	rearmed := createTestWatch(t, backend, ctx, "watch-target-2", "target-1", "identity-2")
	settled, err = backend.AgentWatchTargetDetach(ctx, "target-1", "identity-2", 5000)
	if err != nil || !settled {
		t.Fatalf("permanent detach = %v, %v", settled, err)
	}
	if watch, err := backend.AgentWatchGet(ctx, rearmed.ID); err != nil || watch.Status != store.AgentWatchStatusUnverifiable {
		t.Fatalf("watch after permanent detach = %+v, %v", watch, err)
	}
	row, err = backend.AgentWatchTargetGet(ctx, "target-1")
	if err != nil || !row.Gone || row.Generation != 3 {
		t.Fatalf("target after permanent detach = %+v, %v", row, err)
	}
	// Permanent detach is idempotent.
	settled, err = backend.AgentWatchTargetDetach(ctx, "target-1", "identity-2", 6000)
	if err != nil || settled {
		t.Fatalf("idempotent detach = %v, %v", settled, err)
	}
	if row, err = backend.AgentWatchTargetGet(ctx, "target-1"); err != nil || row.Generation != 3 {
		t.Fatalf("target after idempotent detach = %+v, %v", row, err)
	}
	// Reattaching after permanent detach resurrects as a new generation.
	resurrected, err := backend.AgentWatchTargetAttach(ctx, "target-1", "identity-2", 7000)
	if err != nil || resurrected.Gone || resurrected.Generation != 4 {
		t.Fatalf("resurrect attach = %+v, %v", resurrected, err)
	}
}

func TestAgentWatchTargetOwnerIsolation(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctxA := watchOwnerCtx("owner-a")
	ctxB := watchOwnerCtx("owner-b")
	rowA, err := backend.AgentWatchTargetAttach(ctxA, "target-shared", "identity-a", 1000)
	if err != nil || rowA.Identity != "identity-a" {
		t.Fatalf("attach A = %+v, %v", rowA, err)
	}
	rowB, err := backend.AgentWatchTargetAttach(ctxB, "target-shared", "identity-b", 1000)
	if err != nil || rowB.Identity != "identity-b" {
		t.Fatalf("attach B = %+v, %v", rowB, err)
	}
	if _, err := backend.AgentWatchTargetGet(ctxB, "target-shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchTargetGet(ctxA, "target-missing"); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("missing target get = %v; want not found", err)
	}
	// B's permanent detach must not touch A's target row.
	if settled, err := backend.AgentWatchTargetDetach(ctxB, "target-shared", "identity-b", 2000); err != nil || !settled {
		t.Fatalf("detach B = %v, %v", settled, err)
	}
	rowA, err = backend.AgentWatchTargetGet(ctxA, "target-shared")
	if err != nil || rowA.Identity != "identity-a" || rowA.Gone {
		t.Fatalf("target A after B detach = %+v, %v", rowA, err)
	}
	// And B cannot settle A's watches.
	armed := createTestWatch(t, backend, ctxA, "watch-target-iso", "target-shared", "identity-a")
	if _, err := backend.AgentWatchTargetDetach(ctxB, "target-shared", "identity-a", 3000); err != nil {
		t.Fatal(err)
	}
	if watch, err := backend.AgentWatchGet(ctxA, armed.ID); err != nil || watch.Status != store.AgentWatchStatusArmed {
		t.Fatalf("watch after cross-owner detach = %+v, %v", watch, err)
	}
}

func TestAgentWatchCreateSerializesWithPermanentDetach(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	attachWatchTarget(t, backend, ctx, "target-1", "identity-1")
	const creators = 8
	var wg sync.WaitGroup
	errs := make([]error, creators)
	for index := 0; index < creators; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, errs[index] = backend.AgentWatchCreate(ctx, testWatchRow(fmt.Sprintf("watch-race-%d", index), "target-1"))
		}(index)
	}
	wg.Add(1)
	var detachErr error
	go func() {
		defer wg.Done()
		_, detachErr = backend.AgentWatchTargetDetach(ctx, "target-1", "identity-1", 2000)
	}()
	wg.Wait()
	if detachErr != nil {
		t.Fatalf("concurrent detach: %v", detachErr)
	}
	// Whatever interleaving the row lock produced, no armed watch may
	// survive the completed permanent detach, and every create after it
	// fails with the detached sentinel.
	rows, err := backend.AgentWatchListByTarget(ctx, "target-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Status == store.AgentWatchStatusArmed {
			t.Fatalf("watch %s escaped the permanent detach as armed", row.ID)
		}
	}
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-after-detach", "target-1")); !errors.Is(err, store.ErrAgentWatchDetached) {
		t.Fatalf("create after detach = %v; want ErrAgentWatchDetached", err)
	}
}
