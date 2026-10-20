package store_test

import (
	"context"
	"errors"
	"fmt"
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
	if err := store.EnsureAgentWatchTables(context.Background(), backend.GORM()); err != nil {
		t.Fatalf("ensure agent watch tables: %v", err)
	}
	return backend
}

func watchOwnerCtx(owner string) context.Context {
	return ipc.WithUserID(context.Background(), owner)
}

func testWatchRow(id, target, identity string) model.AgentWatchRow {
	return model.AgentWatchRow{
		ID: id, TargetID: target, Identity: identity, Name: "watch-" + id,
		ConditionJSON: `{"type":"contains","pattern":"ready"}`,
		CreatedAt:     1000, UpdatedAt: 1000,
	}
}

func TestAgentWatchCreateForcesOwnerAndArmedState(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	row := testWatchRow("watch-create-1", "target-1", "identity-1")
	row.OwnerID = "owner-evil"
	row.Status = store.AgentWatchStatusMatched
	row.Revision = 99
	created, err := backend.AgentWatchCreate(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if created.OwnerID != "owner-a" {
		t.Fatalf("owner = %q; want owner-a from context", created.OwnerID)
	}
	if created.Status != store.AgentWatchStatusArmed || created.Revision != 1 {
		t.Fatalf("created = %+v; want armed revision 1", created)
	}
	got, err := backend.AgentWatchGet(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetID != "target-1" || got.Identity != "identity-1" || got.ConditionJSON != row.ConditionJSON {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestAgentWatchCreateValidation(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	cases := map[string]model.AgentWatchRow{
		"empty id":      {TargetID: "t", Identity: "i", ConditionJSON: `{}`},
		"empty target":  {ID: "watch-bad-target", Identity: "i", ConditionJSON: `{}`},
		"empty ident":   {ID: "watch-bad-ident", TargetID: "t", ConditionJSON: `{}`},
		"bad condition": {ID: "watch-bad-cond", TargetID: "t", Identity: "i", ConditionJSON: `{`},
	}
	for name, row := range cases {
		if _, err := backend.AgentWatchCreate(ctx, row); err == nil {
			t.Fatalf("%s: create succeeded", name)
		}
	}
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-dup", "t", "i")); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-dup", "t", "i")); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("duplicate create = %v; want ErrAgentWatchConflict", err)
	}
}

func TestAgentWatchOwnerIsolation(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctxA := watchOwnerCtx("owner-a")
	ctxB := watchOwnerCtx("owner-b")
	created, err := backend.AgentWatchCreate(ctxA, testWatchRow("watch-iso", "target-iso", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchGet(ctxB, created.ID); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("cross-owner get = %v; want ErrAgentWatchNotFound", err)
	}
	rows, err := backend.AgentWatchListByTarget(ctxB, "target-iso")
	if err != nil || len(rows) != 0 {
		t.Fatalf("cross-owner list = %+v, %v; want empty", rows, err)
	}
	if _, err := backend.AgentWatchAdvance(ctxB, created.ID, created.Revision, "identity-1", 10, 2000); !errors.Is(err, store.ErrAgentWatchNotFound) {
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
	created, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-adv", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := backend.AgentWatchAdvance(ctx, created.ID, created.Revision, "identity-1", 10, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.AnchorSeq != 10 || advanced.Revision != 2 {
		t.Fatalf("advanced = %+v", advanced)
	}
	if _, err := backend.AgentWatchAdvance(ctx, created.ID, created.Revision, "identity-1", 20, 2000); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("stale revision advance = %v; want conflict", err)
	}
	if _, err := backend.AgentWatchAdvance(ctx, advanced.ID, advanced.Revision, "identity-1", 5, 2000); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("anchor regression = %v; want conflict", err)
	}
	if _, err := backend.AgentWatchAdvance(ctx, advanced.ID, advanced.Revision, "identity-2", 20, 2000); !errors.Is(err, store.ErrAgentWatchIdentity) {
		t.Fatalf("identity mismatch = %v; want ErrAgentWatchIdentity", err)
	}
	if _, err := backend.AgentWatchAdvance(ctx, "missing", 1, "identity-1", 20, 2000); !errors.Is(err, store.ErrAgentWatchNotFound) {
		t.Fatalf("missing advance = %v; want not found", err)
	}
}

func TestAgentWatchFireWritesWatchAndOutboxTogether(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	created, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-fire", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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
		watch.DeliveryID != "delivery-1" || watch.RunID != "run-delivery-1" || watch.Revision != 2 {
		t.Fatalf("watch after fire = %+v", watch)
	}
	// Same logical fire retried with identical fixed IDs is idempotent.
	retry, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-1", 10))
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if retry.DeliveryID != event.DeliveryID || retry.RunID != event.RunID || retry.CreatedAt != event.CreatedAt {
		t.Fatalf("retry = %+v; want original %+v", retry, event)
	}
	// A double fire with a fresh delivery ID for the same revision is rejected.
	if _, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-2", 10)); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("double fire = %v; want conflict", err)
	}
	// DeliveryID reuse across watches is rejected.
	other, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-fire-2", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchFire(ctx, fireParams(other, "delivery-1", 10)); !errors.Is(err, store.ErrAgentWatchConflict) {
		t.Fatalf("delivery reuse = %v; want conflict", err)
	}
}

func TestAgentWatchFireGuards(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	created, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-guard", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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
	regression := fireParams(created, "delivery-r", 10)
	if _, err := backend.AgentWatchAdvance(ctx, created.ID, created.Revision, "identity-1", 10, 2000); err != nil {
		t.Fatal(err)
	}
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
	created, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-backstop", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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
	created, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-cancel", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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
	repeat := testWatchRow("watch-repeat", "target-1", "identity-1")
	repeat.Repeat = true
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
	created, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-ack", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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
	oneShot, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-oneshot", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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
	for index, id := range []string{"watch-p1", "watch-p2"} {
		row := testWatchRow(id, "target-1", "identity-1")
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
	other, err := backend.AgentWatchCreate(ctxB, testWatchRow("watch-p3", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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
	created, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-unver", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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
	armed, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-unver-2", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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
	expired := testWatchRow("watch-exp-1", "target-1", "identity-1")
	past := int64(1000)
	expired.ExpiresAt = &past
	if _, err := backend.AgentWatchCreate(ctx, expired); err != nil {
		t.Fatal(err)
	}
	future := int64(9999999999)
	later := testWatchRow("watch-exp-2", "target-1", "identity-1")
	later.ExpiresAt = &future
	if _, err := backend.AgentWatchCreate(ctx, later); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-exp-3", "target-1", "identity-1")); err != nil {
		t.Fatal(err)
	}
	otherOwner := testWatchRow("watch-exp-4", "target-1", "identity-1")
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
	created, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-json", "target-1", "identity-1"))
	if err != nil {
		t.Fatal(err)
	}
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

func TestEnsureAgentWatchTablesIsIdempotent(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	if _, err := backend.AgentWatchCreate(ctx, testWatchRow("watch-idem", "target-1", "identity-1")); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureAgentWatchTables(context.Background(), backend.GORM()); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	got, err := backend.AgentWatchGet(ctx, "watch-idem")
	if err != nil || got.TargetID != "target-1" {
		t.Fatalf("watch after re-ensure = %+v, %v", got, err)
	}
	if err := store.EnsureAgentWatchTables(context.Background(), nil); err == nil {
		t.Fatal("ensure with nil db succeeded")
	}
}

func TestAgentWatchTimestampsSurvive(t *testing.T) {
	backend := openAgentWatchStore(t)
	ctx := watchOwnerCtx("owner-a")
	expires := time.Date(2026, time.March, 1, 2, 3, 4, 0, time.UTC).UnixMilli()
	row := testWatchRow("watch-ts", "target-1", "identity-1")
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
