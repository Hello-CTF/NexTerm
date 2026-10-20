package watches

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

type recordingSink struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (s *recordingSink) DeliverWatchEvent(_ context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, event)
	return nil
}

func (s *recordingSink) delivered() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

// ackFailStore injects acknowledgement failures after a successful delivery,
// which is the crash window that produces duplicate deliveries.
type ackFailStore struct {
	Store
	mu       sync.Mutex
	failures int
}

func (s *ackFailStore) AgentWatchEventAck(ctx context.Context, deliveryID string, expectedRevision uint64, now int64) (model.AgentWatchOutboxRow, error) {
	s.mu.Lock()
	if s.failures > 0 {
		s.failures--
		s.mu.Unlock()
		return model.AgentWatchOutboxRow{}, errors.New("injected ack failure")
	}
	s.mu.Unlock()
	return s.Store.AgentWatchEventAck(ctx, deliveryID, expectedRevision, now)
}

func fireOne(t *testing.T, engine *Engine, backend *store.Store, pattern string) Event {
	t.Helper()
	ctx := ownerCtx("owner-a")
	mustCreate(t, engine, ctx, "target-1", containsCondition(pattern))
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte(pattern))
	if err != nil || len(events) != 1 {
		t.Fatalf("fire = %+v, %v", events, err)
	}
	return events[0]
}

func TestDispatchDeliversAndAcks(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	event := fireOne(t, engine, backend, "ready")
	sink := &recordingSink{}
	dispatcher, err := NewDispatcher(backend, sink, Options{Now: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := dispatcher.DispatchOnce(ctx, 10)
	if err != nil || delivered != 1 {
		t.Fatalf("dispatch = %d, %v", delivered, err)
	}
	got := sink.delivered()
	if len(got) != 1 || got[0].DeliveryID != event.DeliveryID || got[0].Kind != EventKind {
		t.Fatalf("delivered = %+v", got)
	}
	stored, err := backend.AgentWatchEventGet(ctx, event.DeliveryID)
	if err != nil || stored.Status != store.AgentWatchEventAcked || stored.DeliveredAt == nil {
		t.Fatalf("acked row = %+v, %v", stored, err)
	}
	delivered, err = dispatcher.DispatchOnce(ctx, 10)
	if err != nil || delivered != 0 {
		t.Fatalf("second dispatch = %d, %v; want 0", delivered, err)
	}
	if len(sink.delivered()) != 1 {
		t.Fatal("acked event was redelivered")
	}
}

func TestDispatchSinkFailureLeavesEventPending(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	event := fireOne(t, engine, backend, "ready")
	sink := &recordingSink{err: errors.New("sink down")}
	dispatcher, err := NewDispatcher(backend, sink, Options{Now: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	if delivered, err := dispatcher.DispatchOnce(ctx, 10); err == nil || delivered != 0 {
		t.Fatalf("dispatch with failing sink = %d, %v", delivered, err)
	}
	stored, err := backend.AgentWatchEventGet(ctx, event.DeliveryID)
	if err != nil || stored.Status != store.AgentWatchEventPending {
		t.Fatalf("event after failed delivery = %+v, %v", stored, err)
	}
	sink.err = nil
	if delivered, err := dispatcher.DispatchOnce(ctx, 10); err != nil || delivered != 1 {
		t.Fatalf("retry dispatch = %d, %v", delivered, err)
	}
}

func TestDispatchDuplicateDeliveryKeepsFixedIDs(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	event := fireOne(t, engine, backend, "ready")
	wrapped := &ackFailStore{Store: backend, failures: 1}
	sink := &recordingSink{}
	dispatcher, err := NewDispatcher(wrapped, sink, Options{Now: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	if delivered, err := dispatcher.DispatchOnce(ctx, 10); err == nil || delivered != 0 {
		t.Fatalf("dispatch with failing ack = %d, %v", delivered, err)
	}
	if delivered, err := dispatcher.DispatchOnce(ctx, 10); err != nil || delivered != 1 {
		t.Fatalf("redelivery dispatch = %d, %v", delivered, err)
	}
	got := sink.delivered()
	if len(got) != 2 {
		t.Fatalf("sink saw %d deliveries; want the duplicate", len(got))
	}
	// CAS is not exactly-once: the duplicate carries the same fixed
	// DeliveryID/RunID so the receiver can dedupe the logical fire.
	if got[0].DeliveryID != event.DeliveryID || got[1].DeliveryID != event.DeliveryID ||
		got[0].RunID != event.RunID || got[1].RunID != event.RunID ||
		got[0].Excerpt != got[1].Excerpt || got[0].MatchedFrom != got[1].MatchedFrom {
		t.Fatalf("duplicate drifted: %+v vs %+v", got[0], got[1])
	}
}

func TestDispatchSkipsCancelledEvents(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("fire = %+v, %v", events, err)
	}
	afterFire, err := engine.Get(ctx, watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Cancel(ctx, watch.ID, afterFire.Revision); err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	dispatcher, err := NewDispatcher(backend, sink, Options{Now: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	if delivered, err := dispatcher.DispatchOnce(ctx, 10); err != nil || delivered != 0 {
		t.Fatalf("dispatch after cancel = %d, %v; want 0", delivered, err)
	}
	if len(sink.delivered()) != 0 {
		t.Fatal("cancelled event was delivered")
	}
}

func TestDispatchRejectsCorruptEnvelope(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	corrupt := model.AgentWatchOutboxRow{
		DeliveryID: "delivery-corrupt", RunID: "run-corrupt", WatchID: "watch-corrupt",
		FireSeq: 1, OwnerID: "owner-a", Kind: EventKind, Status: store.AgentWatchEventPending,
		Revision: 1, EventJSON: "{", CreatedAt: 1000, UpdatedAt: 1000,
	}
	if err := backend.GORM().Create(&corrupt).Error; err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	dispatcher, err := NewDispatcher(backend, sink, Options{Now: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.DispatchOnce(ctx, 10); !errors.Is(err, ErrEventCorrupt) {
		t.Fatalf("corrupt dispatch = %v; want ErrEventCorrupt", err)
	}
	mismatched := corrupt
	mismatched.DeliveryID = "delivery-mismatch"
	mismatched.FireSeq = 2
	mismatched.CreatedAt = 500
	mismatched.UpdatedAt = 500
	mismatched.EventJSON = `{"kind":"watchEvent","deliveryId":"other","watchId":"watch-corrupt"}`
	if err := backend.GORM().Create(&mismatched).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.DispatchOnce(ctx, 10); !errors.Is(err, ErrEventCorrupt) {
		t.Fatalf("mismatched dispatch = %v; want ErrEventCorrupt", err)
	}
	if len(sink.delivered()) != 0 {
		t.Fatal("corrupt event reached the sink")
	}
}

func TestDispatchIsOwnerScoped(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	fireOne(t, engine, backend, "ready")
	sink := &recordingSink{}
	dispatcher, err := NewDispatcher(backend, sink, Options{Now: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := dispatcher.DispatchOnce(ownerCtx("owner-b"), 10)
	if err != nil || delivered != 0 {
		t.Fatalf("cross-owner dispatch = %d, %v; want 0", delivered, err)
	}
	if len(sink.delivered()) != 0 {
		t.Fatal("cross-owner delivery reached the sink")
	}
}
