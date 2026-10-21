package watches

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// scriptSource is a deterministic synthetic Source: a scripted sequence of
// chunks with optional error/EOF injection and position skips. It is a test
// double, not external CLI validation. When gate is non-nil, the first Chunk
// call blocks until the gate closes, which pins watch creation before any
// output is consumed. gateAfter blocks before the step at that index.
type scriptSource struct {
	mu        sync.Mutex
	steps     []scriptStep
	endSeq    int64
	tail      []byte
	gate      chan struct{}
	gateAfter int
	calls     int
	observed  map[int64]byte
	closed    bool
}

type scriptStep struct {
	data []byte
	skip int64
	err  error
}

func (s *scriptSource) Chunk(ctx context.Context, fromSeq int64) ([]byte, int64, error) {
	s.mu.Lock()
	if s.gate != nil && s.calls == s.gateAfter {
		gate := s.gate
		s.gate = nil
		s.mu.Unlock()
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, fromSeq, ctx.Err()
		}
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	if s.calls >= len(s.steps) {
		return nil, fromSeq, io.EOF
	}
	step := s.steps[s.calls]
	s.calls++
	if step.err != nil {
		return nil, fromSeq, step.err
	}
	if s.observed == nil {
		s.observed = make(map[int64]byte)
	}
	for index, value := range step.data {
		s.observed[fromSeq+int64(index)] = value
	}
	return step.data, fromSeq + int64(len(step.data)) + step.skip, nil
}

func (s *scriptSource) End(ctx context.Context) (int64, error) { return s.endSeq, nil }

func (s *scriptSource) Tail(ctx context.Context, beforeSeq int64, maxBytes int) ([]byte, error) {
	if beforeSeq <= 0 || maxBytes <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stream := make(map[int64]byte, len(s.tail)+len(s.observed))
	base := s.endSeq - int64(len(s.tail))
	for index, value := range s.tail {
		stream[base+int64(index)] = value
	}
	for position, value := range s.observed {
		stream[position] = value
	}
	start := beforeSeq - int64(maxBytes)
	if start < 0 {
		start = 0
	}
	var tail []byte
	for position := start; position < beforeSeq; position++ {
		value, ok := stream[position]
		if !ok {
			tail = tail[:0]
			continue
		}
		tail = append(tail, value)
	}
	return tail, nil
}

func (s *scriptSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// chanSink signals every delivered envelope on a channel for deterministic
// waits.
type chanSink struct {
	mu         sync.Mutex
	deliveries []ToolData
	ch         chan ToolData
	err        error
}

func newChanSink() *chanSink {
	return &chanSink{ch: make(chan ToolData, 32)}
}

func (s *chanSink) DeliverWatchToolData(_ context.Context, data ToolData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.deliveries = append(s.deliveries, data)
	s.ch <- data
	return nil
}

func (s *chanSink) delivered() []ToolData {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ToolData(nil), s.deliveries...)
}

func (s *chanSink) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func waitDelivery(t *testing.T, sink *chanSink) ToolData {
	t.Helper()
	select {
	case data := <-sink.ch:
		return data
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for watch delivery")
		return ToolData{}
	}
}

func newTestExecutor(t *testing.T, backend *store.Store, sink Sink) *Executor {
	t.Helper()
	executor, err := NewExecutor(ownerCtx("owner-a"), backend, sink, ExecutorOptions{
		Now: newTestClock().Now, DispatchInterval: 10 * time.Millisecond, RetryBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

// attachTarget attaches with the current durable generation as the
// incarnation fence: sequential tests always present the generation the
// locked row currently carries, which is exactly the fencing contract.
func attachTarget(t *testing.T, executor *Executor, ctx context.Context, targetID, identity string, source Source) {
	t.Helper()
	generation := uint64(0)
	if row, err := executor.store.AgentWatchTargetGet(ctx, targetID); err == nil {
		generation = row.Generation
	}
	if err := executor.AttachTarget(ctx, targetID, identity, source, generation); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorFeedsObservationsAndDeliversToolData(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	sink := newChanSink()
	executor := newTestExecutor(t, backend, sink)
	gate := make(chan struct{})
	source := &scriptSource{gate: gate, steps: []scriptStep{
		{data: []byte("not yet\n")},
		{data: []byte("xxready\n")},
	}}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	watch, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	close(gate)
	if err := executor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	data := waitDelivery(t, sink)
	if data.Channel != "tool" || !data.Untrusted {
		t.Fatalf("watch event must travel as untrusted tool data: %+v", data)
	}
	event := data.Event
	if event.Kind != EventKind || event.DeliveryID == "" || event.RunID == "" ||
		event.WatchID != watch.ID || event.MatchedFrom != 10 || event.MatchedTo != 15 {
		t.Fatalf("delivered event = %+v", event)
	}
	// The event is acknowledged: wait for the durable ack, then a later pass
	// delivers nothing new. (Two deliveries would be legal under at-least-once,
	// so the test waits for the ack instead of racing it.)
	deadline := time.Now().Add(10 * time.Second)
	for {
		row, err := backend.AgentWatchEventGet(ctx, event.DeliveryID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status == store.AgentWatchEventAcked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("event was not acked: %+v", row)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := executor.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sink.delivered(); len(got) != 1 {
		t.Fatalf("deliveries after ack = %d; want 1", len(got))
	}
	if err := executor.DetachTarget(ctx, "target-1", true); err != nil {
		t.Fatal(err)
	}
	// The fired watch is terminal (matched); a permanent detach settles any
	// remaining armed watches and closes the source.
	after, err := backend.AgentWatchGet(ctx, watch.ID)
	if err != nil || after.Status != store.AgentWatchStatusMatched {
		t.Fatalf("watch after permanent detach = %+v, %v", after, err)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorWatchFillsAnchorAndLeftContext(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	executor := newTestExecutor(t, backend, newChanSink())
	source := &scriptSource{endSeq: 10, tail: []byte("12345678")}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	watch, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	if watch.AnchorSeq != 10 || string(watch.Carry) != "" {
		t.Fatalf("watch anchor = %d carry %q; want 10 from the source end", watch.AnchorSeq, watch.Carry)
	}
	row, err := backend.AgentWatchGet(ctx, watch.ID)
	if err != nil || string(row.ContextBlob) != "12345678" {
		t.Fatalf("watch left context = %q, %v", row.ContextBlob, err)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorRestartResumesPendingDeliveryWithFixedIDs(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")

	// The first executor fires but stops before its dispatch loop delivers.
	gate := make(chan struct{})
	source := &scriptSource{gate: gate, steps: []scriptStep{{data: []byte("ready")}}}
	first := newTestExecutor(t, backend, newChanSink())
	attachTarget(t, first, ctx, "target-1", "identity-1", source)
	if _, err := first.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")}); err != nil {
		t.Fatal(err)
	}
	close(gate)
	// The first executor fires but never dispatches: the event stays pending
	// for the next executor to resume with the same fixed IDs.
	var pending []model.AgentWatchOutboxRow
	deadline := time.Now().Add(10 * time.Second)
	for {
		pending, err = backend.AgentWatchEventPendingList(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch did not fire: %+v", pending)
		}
		time.Sleep(5 * time.Millisecond)
	}
	fixed := pending[0]
	if err := first.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	// The second executor resumes the pending event with the same fixed IDs.
	sink := newChanSink()
	second := newTestExecutor(t, backend, sink)
	if err := second.Start(ctx); err != nil {
		t.Fatal(err)
	}
	data := waitDelivery(t, sink)
	if data.Event.DeliveryID != fixed.DeliveryID || data.Event.RunID != fixed.RunID {
		t.Fatalf("restarted delivery = %s/%s; want fixed %s/%s", data.Event.DeliveryID, data.Event.RunID, fixed.DeliveryID, fixed.RunID)
	}
	if data.Channel != "tool" || !data.Untrusted {
		t.Fatalf("restarted delivery is not untrusted tool data: %+v", data)
	}
	if err := second.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorDuplicateDeliveryDeduplicatedByConsumer(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	gate := make(chan struct{})
	source := &scriptSource{gate: gate, steps: []scriptStep{{data: []byte("ready")}}}

	// First executor: the delivery succeeds but the acknowledgement fails —
	// the crash window that forces a duplicate redelivery.
	sink := newChanSink()
	wrapped := &ackFailStore{Store: backend, failures: 100}
	first, err := NewExecutor(ctx, wrapped, sink, ExecutorOptions{
		Now: newTestClock().Now, DispatchInterval: 10 * time.Millisecond, RetryBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	attachTarget(t, first, ctx, "target-1", "identity-1", source)
	if _, err := first.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")}); err != nil {
		t.Fatal(err)
	}
	close(gate)
	if err := first.Start(ctx); err != nil {
		t.Fatal(err)
	}
	firstDelivery := waitDelivery(t, sink)
	if err := first.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := backend.AgentWatchEventPendingList(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after lost ack = %+v, %v", pending, err)
	}

	// Second executor redelivers the same logical fire: the consumer dedupes
	// by the fixed DeliveryID, so only one logical event exists.
	second := newTestExecutor(t, backend, sink)
	if err := second.Start(ctx); err != nil {
		t.Fatal(err)
	}
	duplicate := waitDelivery(t, sink)
	if duplicate.Event.DeliveryID != firstDelivery.Event.DeliveryID ||
		duplicate.Event.RunID != firstDelivery.Event.RunID {
		t.Fatalf("duplicate drifted: %s/%s vs %s/%s",
			duplicate.Event.DeliveryID, duplicate.Event.RunID, firstDelivery.Event.DeliveryID, firstDelivery.Event.RunID)
	}
	seen := map[string]int{}
	for _, data := range sink.delivered() {
		seen[data.Event.DeliveryID]++
	}
	if len(seen) != 1 || seen[firstDelivery.Event.DeliveryID] != 2 {
		t.Fatalf("consumer dedupe by DeliveryID failed: %+v", seen)
	}
	if err := second.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorEOFDefersThenFiresAtEOF(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	sink := newChanSink()
	executor := newTestExecutor(t, backend, sink)
	gate := make(chan struct{})
	source := &scriptSource{gate: gate, steps: []scriptStep{
		{data: []byte("ready")},
	}}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	if _, err := executor.Watch(ctx, WatchParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `ready$`}}); err != nil {
		t.Fatal(err)
	}
	close(gate)
	if err := executor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// The source ends after "ready": the deferred right-edge match fires at
	// EOF with end-of-input semantics.
	data := waitDelivery(t, sink)
	if data.Event.MatchedFrom != 0 || data.Event.MatchedTo != 5 {
		t.Fatalf("EOF delivery = %+v", data.Event)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorGapSettlesUnverifiable(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	sink := newChanSink()
	executor := newTestExecutor(t, backend, sink)
	gate := make(chan struct{})
	source := &scriptSource{gate: gate, steps: []scriptStep{
		{data: []byte("ab"), skip: 6},
		{data: []byte("ready")},
	}}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	watch, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	close(gate)
	if err := executor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// The skipped positions are an output gap: the watch settles and nothing
	// is delivered.
	deadline := time.Now().Add(10 * time.Second)
	for {
		after, err := backend.AgentWatchGet(ctx, watch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status == store.AgentWatchStatusUnverifiable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch did not settle: %+v", after)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := sink.delivered(); len(got) != 0 {
		t.Fatalf("gap produced deliveries: %+v", got)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorRetryAfterSourceError(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	sink := newChanSink()
	executor := newTestExecutor(t, backend, sink)
	gate := make(chan struct{})
	source := &scriptSource{gate: gate, steps: []scriptStep{
		{err: errors.New("source hiccup")},
		{data: []byte("ready")},
	}}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	if _, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")}); err != nil {
		t.Fatal(err)
	}
	close(gate)
	if err := executor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	data := waitDelivery(t, sink)
	if data.Event.MatchedFrom != 0 || data.Event.MatchedTo != 5 {
		t.Fatalf("delivery after retry = %+v", data.Event)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorDetachPermanentStopsAndSettles(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	executor := newTestExecutor(t, backend, newChanSink())
	gate := make(chan struct{})
	source := &scriptSource{gate: gate, steps: []scriptStep{{data: []byte("ready")}}}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	watch, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	// Detach cancels the gated runner: the source never delivers, and the
	// armed watch is settled.
	if err := executor.DetachTarget(ctx, "target-1", true); err != nil {
		t.Fatal(err)
	}
	after, err := backend.AgentWatchGet(ctx, watch.ID)
	if err != nil || after.Status != store.AgentWatchStatusUnverifiable {
		t.Fatalf("watch after detach = %+v, %v", after, err)
	}
	source.mu.Lock()
	closed := source.closed
	source.mu.Unlock()
	if !closed {
		t.Fatal("source not closed after permanent detach")
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
