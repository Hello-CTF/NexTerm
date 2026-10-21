package watches

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// detachFailStore fails the first N durable target detaches.
type detachFailStore struct {
	Store
	mu       sync.Mutex
	failures int
}

func (s *detachFailStore) AgentWatchTargetDetach(ctx context.Context, targetID, identity string, generation uint64, now int64) (bool, error) {
	s.mu.Lock()
	if s.failures > 0 {
		s.failures--
		s.mu.Unlock()
		return false, errors.New("injected detach failure")
	}
	s.mu.Unlock()
	return s.Store.AgentWatchTargetDetach(ctx, targetID, identity, generation, now)
}

// fireFailStore fails the first N watch fires.
type fireFailStore struct {
	Store
	mu       sync.Mutex
	failures int
}

func (s *fireFailStore) AgentWatchFire(ctx context.Context, params store.AgentWatchFireParams) (model.AgentWatchOutboxRow, error) {
	s.mu.Lock()
	if s.failures > 0 {
		s.failures--
		s.mu.Unlock()
		return model.AgentWatchOutboxRow{}, errors.New("injected fire failure")
	}
	s.mu.Unlock()
	return s.Store.AgentWatchFire(ctx, params)
}

func TestWatchCreatedAtUTF8SplitDoesNotPanic(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	// The left context is a single lead byte: the next continuation byte
	// makes the whole buffer an incomplete sequence.
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", AnchorSeq: 1,
		LeftContext: []byte{0xE7}, Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 1, []byte{0x95})
	if err != nil || len(events) != 0 {
		t.Fatalf("continuation chunk = %+v, %v", events, err)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusArmed || after.AnchorSeq != 0 {
		t.Fatalf("watch after split = %+v, %v", after, err)
	}
	// The incomplete rune is preserved whole and the stream continues.
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 2, []byte{0x8C}); err != nil {
		t.Fatal(err)
	}
	events, err = engine.Observe(ctx, "target-1", "identity-1", 3, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("post-split observe = %+v, %v", events, err)
	}
}

func TestWatchCreatedAtUTF8SplitDoesNotPanicRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", AnchorSeq: 1,
		LeftContext: []byte{0xE7}, Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 1, []byte{0x95}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	engine = newRestartEngine(t, backend, clock)
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 2, []byte{0x8C}); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 3, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("post-restart split observe = %+v, %v", events, err)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusMatched {
		t.Fatalf("watch after restarted split = %+v, %v", after, err)
	}
}

func TestExecutorWatchCreatedAtUTF8SplitDoesNotPanic(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	executor := newTestExecutor(t, backend, newChanSink())
	// The source ends right after the lead byte; Watch is created before the
	// continuation arrives.
	source := &scriptSource{endSeq: 1, tail: []byte{0xE7}, steps: []scriptStep{
		{data: []byte{0x95}},
		{data: []byte{0x8C}},
		{data: []byte("ready")},
	}}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	watch, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	if watch.AnchorSeq != 0 || string(watch.Carry) != string([]byte{0xE7}) {
		t.Fatalf("watch anchor = %d carry %q; want 0 with the incomplete rune in carry", watch.AnchorSeq, watch.Carry)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorRestartResumesMatchedRepeat(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")

	// First executor: the repeat watch fires and stops before consuming 5..7.
	first := newTestExecutor(t, backend, newChanSink())
	gate := make(chan struct{})
	source := &scriptSource{gate: gate, steps: []scriptStep{{data: []byte("ready")}}}
	attachTarget(t, first, ctx, "target-1", "identity-1", source)
	watch, err := first.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready"), Repeat: true})
	if err != nil {
		t.Fatal(err)
	}
	close(gate)
	deadline := time.Now().Add(10 * time.Second)
	for {
		after, err := backend.AgentWatchGet(ctx, watch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status == store.AgentWatchStatusMatched {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch did not fire: %+v", after)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := first.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	// Second source reports end 7. Recovery must resume from the matched
	// repeat's evaluated position (5), not the source end: consume XX, ack
	// re-arms, and the second ready fires.
	sink := newChanSink()
	second := newTestExecutor(t, backend, sink)
	secondGate := make(chan struct{})
	secondSource := &scriptSource{endSeq: 7, gate: secondGate, gateAfter: 1, steps: []scriptStep{
		{data: []byte("XX")},
		{data: []byte("ready")},
	}}
	attachTarget(t, second, ctx, "target-1", "identity-1", secondSource)
	// The runner consumes XX while the watch is matched; the ack re-arms it,
	// and only then does the second ready flow.
	if _, err := second.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	close(secondGate)
	if err := second.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// The first delivery is the resumed pending event; the second is the new
	// fire from the re-armed watch.
	resumed := waitDelivery(t, sink)
	if resumed.Event.MatchedFrom != 0 || resumed.Event.MatchedTo != 5 {
		t.Fatalf("resumed delivery = %+v; want 0..5", resumed.Event)
	}
	refire := waitDelivery(t, sink)
	if refire.Event.MatchedFrom != 7 || refire.Event.MatchedTo != 12 {
		t.Fatalf("second fire = %+v; want 7..12", refire.Event)
	}
	after, err := backend.AgentWatchGet(ctx, watch.ID)
	if err != nil || after.Status == store.AgentWatchStatusUnverifiable {
		t.Fatalf("watch after resumed repeat = %+v, %v", after, err)
	}
	if err := second.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorDetachRetryAfterFailure(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	wrapped := &detachFailStore{Store: backend, failures: 1}
	executor, err := NewExecutor(ctx, wrapped, newChanSink(), ExecutorOptions{
		Now: newTestClock().Now, DispatchInterval: 10 * time.Millisecond, RetryBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := &scriptSource{gate: make(chan struct{}), steps: []scriptStep{{data: []byte("ready")}}}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	watch, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	// First attempt fails durably; the retry must not report success.
	if err := executor.DetachTarget(ctx, "target-1", true); err == nil {
		t.Fatal("permanent detach reported success despite the injected failure")
	}
	row, err := backend.AgentWatchTargetGet(ctx, "target-1")
	if err != nil || row.Gone {
		t.Fatalf("target after failed detach = %+v, %v", row, err)
	}
	if after, err := backend.AgentWatchGet(ctx, watch.ID); err != nil || after.Status != store.AgentWatchStatusArmed {
		t.Fatalf("watch after failed detach = %+v, %v", after, err)
	}
	// The retry completes the durable transition.
	if err := executor.DetachTarget(ctx, "target-1", true); err != nil {
		t.Fatalf("retried permanent detach: %v", err)
	}
	row, err = backend.AgentWatchTargetGet(ctx, "target-1")
	if err != nil || !row.Gone {
		t.Fatalf("target after retried detach = %+v, %v", row, err)
	}
	if after, err := backend.AgentWatchGet(ctx, watch.ID); err != nil || after.Status != store.AgentWatchStatusUnverifiable {
		t.Fatalf("watch after retried detach = %+v, %v", after, err)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorEOFFaultInjection(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	wrapped := &fireFailStore{Store: backend, failures: 1}
	sink := newChanSink()
	executor, err := NewExecutor(ctx, wrapped, sink, ExecutorOptions{
		Now: newTestClock().Now, DispatchInterval: 10 * time.Millisecond, RetryBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := &scriptSource{steps: []scriptStep{{data: []byte("ready")}}}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	if _, err := executor.Watch(ctx, WatchParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `ready$`}}); err != nil {
		t.Fatal(err)
	}
	if err := executor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// The first EOF fire attempt fails transiently; the runner retries and
	// the deferred event is delivered, not silently dropped.
	data := waitDelivery(t, sink)
	if data.Event.MatchedFrom != 0 || data.Event.MatchedTo != 5 {
		t.Fatalf("EOF delivery after retry = %+v", data.Event)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
