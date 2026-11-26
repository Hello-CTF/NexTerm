package watches

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

type repeatHarness struct {
	t          *testing.T
	ctx        context.Context
	clock      *testClock
	engine     *Engine
	dispatcher *Dispatcher
	sink       *recordingSink
	watchID    string
	seq        int64
}

func newRepeatHarness(t *testing.T) *repeatHarness {
	t.Helper()
	clock := newTestClock()
	engine, backend := newTestEngine(t, clock)
	sink := &recordingSink{}
	dispatcher, err := NewDispatcher(backend, sink, Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerCtx("owner-a")
	attachEngine(t, engine, ctx, "target-1", "identity-1")
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready"), Repeat: true})
	if err != nil {
		t.Fatal(err)
	}
	return &repeatHarness{t: t, ctx: ctx, clock: clock, engine: engine, dispatcher: dispatcher, sink: sink, watchID: watch.ID}
}

// output feeds matching output and returns how many events it fired.
func (h *repeatHarness) output() int {
	h.t.Helper()
	events, err := h.engine.Observe(h.ctx, "target-1", "identity-1", h.seq, []byte("ready"))
	if err != nil {
		h.t.Fatal(err)
	}
	h.seq += int64(len("ready"))
	return len(events)
}

func (h *repeatHarness) dispatch() {
	h.t.Helper()
	if _, err := h.dispatcher.DispatchOnce(h.ctx, 64); err != nil {
		h.t.Fatal(err)
	}
}

func (h *repeatHarness) rearm() int64 {
	h.t.Helper()
	count, err := h.engine.RearmDue(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return count
}

func (h *repeatHarness) watch() Watch {
	h.t.Helper()
	watch, err := h.engine.Get(h.ctx, h.watchID)
	if err != nil {
		h.t.Fatal(err)
	}
	return watch
}

// A repeat watch acknowledged right after it fired stays matched, consuming
// output, until the minimum re-arm interval since the fire has passed.
func TestRepeatWatchWaitsTheMinimumIntervalBeforeReArming(t *testing.T) {
	h := newRepeatHarness(t)
	if fired := h.output(); fired != 1 {
		t.Fatalf("first output fired %d events", fired)
	}
	h.dispatch()
	if got := h.watch(); got.Status != StatusMatched {
		t.Fatalf("watch after an early ack = %s, want matched", got.Status)
	}
	if fired := h.output(); fired != 0 {
		t.Fatal("output during the re-arm interval fired the watch")
	}
	h.clock.Advance(DefaultRepeatMinInterval - time.Millisecond)
	if count := h.rearm(); count != 0 {
		t.Fatalf("re-armed %d watches before the interval passed", count)
	}
	h.clock.Advance(time.Millisecond)
	if count := h.rearm(); count != 1 {
		t.Fatalf("re-armed %d watches after the interval, want 1", count)
	}
	if got := h.watch(); got.Status != StatusArmed || got.AnchorSeq != h.seq {
		t.Fatalf("re-armed watch = %+v, want armed at %d", got, h.seq)
	}
	if fired := h.output(); fired != 1 {
		t.Fatal("re-armed watch did not fire on new output")
	}
	if delivered := len(h.sink.delivered()); delivered != 1 {
		t.Fatalf("delivered %d events before the second dispatch, want 1", delivered)
	}
}

// A repeat watch settles as expired, with a reason the watch panel shows,
// once it fired DefaultMaxRepeatFires times; later output fires nothing.
func TestRepeatWatchSettlesAtTheFireCap(t *testing.T) {
	h := newRepeatHarness(t)
	for fire := 1; fire <= DefaultMaxRepeatFires; fire++ {
		if fired := h.output(); fired != 1 {
			t.Fatalf("fire %d: output fired %d events", fire, fired)
		}
		h.dispatch()
		h.clock.Advance(DefaultRepeatMinInterval)
		h.rearm()
	}
	got := h.watch()
	if got.Status != StatusExpired || got.LastError != RepeatLimitReason || got.FireSeq != DefaultMaxRepeatFires {
		t.Fatalf("watch at the cap = %+v, want expired with %q after %d fires", got, RepeatLimitReason, DefaultMaxRepeatFires)
	}
	if fired := h.output(); fired != 0 {
		t.Fatal("a capped watch fired again")
	}
	if delivered := len(h.sink.delivered()); delivered != DefaultMaxRepeatFires {
		t.Fatalf("delivered %d events, want %d", delivered, DefaultMaxRepeatFires)
	}
}

// The store acknowledgement without a policy keeps re-arming at once, so
// callers that pass no bounds keep the earlier contract.
func TestAckWithoutPolicyReArmsImmediately(t *testing.T) {
	clock := newTestClock()
	engine, backend := newTestEngine(t, clock)
	ctx := ownerCtx("owner-a")
	attachEngine(t, engine, ctx, "target-1", "identity-1")
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready"), Repeat: true})
	if err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("fire = %+v, %v", events, err)
	}
	if _, err := backend.AgentWatchEventAckPolicy(ctx, events[0].DeliveryID, 1, clock.Now().UnixMilli(), store.AgentWatchRepeatPolicy{}); err != nil {
		t.Fatal(err)
	}
	if got, err := engine.Get(ctx, watch.ID); err != nil || got.Status != StatusArmed {
		t.Fatalf("watch after an unbounded ack = %+v, %v", got, err)
	}
}

// The running executor re-arms a delivered repeat watch on its own tick once
// the re-arm interval passed, without any direct RearmDue call.
func TestExecutorTickReArmsRepeatWatchAfterTheInterval(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	clock := newTestClock()
	sink := newChanSink()
	executor, err := NewExecutor(ctx, backend, sink, ExecutorOptions{
		Now: clock.Now, DispatchInterval: 5 * time.Millisecond, RetryBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	attachTarget(t, executor, ctx, "target-1", "identity-1", &scriptSource{gate: gate, steps: []scriptStep{{data: []byte("ready")}}})
	watch, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready"), Repeat: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.Stop(ctx) })
	close(gate)
	delivered := waitDelivery(t, sink)
	// Wait for the acknowledgement, which leaves the watch matched inside
	// the interval, before moving the clock past it.
	for deadline := time.Now().Add(10 * time.Second); ; {
		event, err := backend.AgentWatchEventGet(ctx, delivered.Event.DeliveryID)
		if err != nil {
			t.Fatal(err)
		}
		if event.Status == store.AgentWatchEventAcked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("event was not acknowledged: %+v", event)
		}
		time.Sleep(time.Millisecond)
	}
	if got, err := backend.AgentWatchGet(ctx, watch.ID); err != nil || got.Status != store.AgentWatchStatusMatched {
		t.Fatalf("watch after an early ack = %+v, %v", got, err)
	}
	clock.Advance(DefaultRepeatMinInterval)
	for deadline := time.Now().Add(10 * time.Second); ; {
		got, err := backend.AgentWatchGet(ctx, watch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == store.AgentWatchStatusArmed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("executor tick did not re-arm the watch: %+v", got)
		}
		time.Sleep(time.Millisecond)
	}
}
