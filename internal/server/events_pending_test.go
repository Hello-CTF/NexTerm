package server

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// gatedEventFilter parks the first filter evaluation (and every later one)
// until unblocked, so a test can drive a subscription into a chosen backlog.
type gatedEventFilter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedEventFilter() *gatedEventFilter {
	return &gatedEventFilter{started: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedEventFilter) filter(ipc.Event) bool {
	g.once.Do(func() { close(g.started) })
	<-g.release
	return true
}

func (g *gatedEventFilter) unblock() { close(g.release) }

// Saturation: a parked filter plus a burst larger than the pending bound must
// drop the overflowed subscriber (slow hook), keep every Emit non-blocking,
// drain the backlog exactly once, and resync a fresh subscriber.
func TestEventBrokerFilteredPendingOverflowDropsAndResyncs(t *testing.T) {
	broker := NewEventBroker()
	broker.QueueSize = 2 // pending bound = 2*QueueSize = 4
	var slowDrops atomic.Int32
	broker.OnSlowSubscriber = func() { slowDrops.Add(1) }

	gate := newGatedEventFilter()
	filtered, unsubscribeFiltered, err := broker.subscribe(0, false, false, gate.filter)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeFiltered()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Start one Emit first and wait until the worker is inside the filter, so
	// the overflow below cannot race the worker's first evaluation.
	first := make(chan error, 1)
	go func() {
		first <- broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 1}})
	}()
	select {
	case <-gate.started:
	case <-ctx.Done():
		t.Fatal("first Emit did not reach the filter")
	}
	// The worker is parked inside the first filter evaluation; the next
	// 2*QueueSize-1 fills the pending backlog and the last one overflows it.
	results := make(chan error, 5)
	for seq := 2; seq <= 6; seq++ {
		go func(seq int) {
			results <- broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": seq}})
		}(seq)
	}
	// The overflow drop must not wait for the parked backlog.
	select {
	case <-filtered.done:
	case <-time.After(2 * time.Second):
		t.Fatal("overflowed subscriber was not dropped; pending backlog is unbounded")
	}
	if got := slowDrops.Load(); got != 1 {
		t.Fatalf("slow subscriber hook fired %d times, want 1", got)
	}
	// Unblock: the retained backlog drains exactly once (no double-complete panic).
	gate.unblock()
	select {
	case err := <-first:
		if err != nil {
			t.Fatalf("first Emit = %v, want nil (event stays buffered)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first Emit did not complete after unblock")
	}
	for seq := 2; seq <= 6; seq++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("Emit %d = %v, want nil (event stays buffered)", seq, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("Emit %d did not complete after unblock", seq)
		}
	}
	// A fresh subscriber resyncs instead of replaying the overflowed backlog.
	resynced, unsubscribeResynced, err := broker.subscribe(0, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeResynced()
	marker := drainReplay(t, resynced, 1)
	if !marker[0].Resync {
		t.Fatalf("resync marker = %+v, want resync", marker[0])
	}
}

// Slow consumer: within the bound, a parked filter leaves the backlog pending
// without stalling Emit; once released, every event is delivered in FIFO order.
func TestEventBrokerFilteredSlowConsumerDeliversFIFOInOrder(t *testing.T) {
	broker := NewEventBroker()
	broker.QueueSize = 8
	gate := newGatedEventFilter()
	filtered, unsubscribe, err := broker.subscribe(0, false, false, gate.filter)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Start one Emit first and wait until the worker is inside the filter, so
	// the backlog below cannot race the worker's first evaluation.
	first := make(chan error, 1)
	go func() {
		first <- broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 1}})
	}()
	select {
	case <-gate.started:
	case <-ctx.Done():
		t.Fatal("first Emit did not reach the filter")
	}
	// The worker is parked; the rest of the backlog stays pending, not stalled.
	results := make(chan error, 4)
	for seq := 2; seq <= 5; seq++ {
		go func(seq int) {
			results <- broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": seq}})
		}(seq)
	}
	for seq := 2; seq <= 5; seq++ {
		select {
		case err := <-results:
			t.Fatalf("Emit %d returned before the backlog drained: %v", seq-1, err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	gate.unblock()
	select {
	case err := <-first:
		if err != nil {
			t.Fatalf("first Emit = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first Emit did not complete after unblock")
	}
	for seq := 2; seq <= 5; seq++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("Emit %d = %v", seq, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("Emit %d did not complete after unblock", seq)
		}
	}
	received := drainReplay(t, filtered, 5)
	for index, envelope := range received {
		if envelope.ID != uint64(index+1) {
			t.Fatalf("delivered IDs = %+v, want [1 2 3 4 5] in FIFO order", received)
		}
	}
}

// Cancellation: a parked Emit returns ctx.Err() promptly once waitFor honors
// cancellation; the delivery still completes exactly once in the background,
// the subscriber stays alive, and the canceled event is never pushed.
func TestEventBrokerFilteredEmitWaitForContextCancel(t *testing.T) {
	broker := NewEventBroker()
	gate := newGatedEventFilter()
	filtered, unsubscribe, err := broker.subscribe(0, false, false, gate.filter)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 1}})
	}()
	select {
	case <-gate.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Emit did not reach the filter")
	}
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("Emit error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Emit did not return after ctx cancel; waitFor ignores cancellation")
	}
	gate.unblock()
	select {
	case <-filtered.done:
		t.Fatal("canceled delivery dropped the subscriber")
	case <-time.After(200 * time.Millisecond):
	}
	if got := len(filtered.queue); got != 0 {
		t.Fatalf("canceled event reached the queue: %d messages", got)
	}
	// The worker stays healthy: the next event is delivered normally.
	if err := broker.Emit(context.Background(), ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 2}}); err != nil {
		t.Fatal(err)
	}
	received := drainReplay(t, filtered, 1)
	if !strings.Contains(string(received[0].Payload), `"seq":2`) {
		t.Fatalf("delivered payload = %s, want seq 2", received[0].Payload)
	}
}

// Timeout: a stuck ownership lookup is bounded; it fails closed (the event is
// not delivered), the Emit stays non-blocking, and the subscriber survives.
func TestEventBrokerOwnerFilterLookupTimeoutFailClosed(t *testing.T) {
	original := eventOwnerLookupTimeout
	eventOwnerLookupTimeout = 50 * time.Millisecond
	defer func() { eventOwnerLookupTimeout = original }()

	broker := NewEventBroker()
	server := &Server{assetOwner: func(ctx context.Context, assetID string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	filtered, unsubscribe, err := broker.subscribe(0, false, false, server.eventFilter(&account.Identity{UserID: "u-1", Role: account.RoleUser}))
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	started := time.Now()
	if err := broker.Emit(context.Background(), ipc.Event{Event: ipc.TopicConnectProgress, Payload: ipc.ConnectProgressEvent{AssetID: "asset-1"}}); err != nil {
		t.Fatalf("Emit = %v, want nil (event stays buffered)", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("stuck ownership lookup bounded after %v, want a bounded timeout", elapsed)
	}
	if got := len(filtered.queue); got != 0 {
		t.Fatalf("timed-out ownership lookup delivered the event: %d messages", got)
	}
	select {
	case <-filtered.done:
		t.Fatal("timed-out lookup dropped the subscriber")
	default:
	}
}

// Owner behavior: a slow-but-successful ownership lookup within the timeout
// still delivers to the owning subscriber only.
func TestEventBrokerOwnerFilterLookupSuccessWithinTimeout(t *testing.T) {
	original := eventOwnerLookupTimeout
	eventOwnerLookupTimeout = 250 * time.Millisecond
	defer func() { eventOwnerLookupTimeout = original }()

	broker := NewEventBroker()
	server := &Server{assetOwner: func(ctx context.Context, assetID string) (string, error) {
		if assetID == "asset-slow" {
			time.Sleep(100 * time.Millisecond) // within the timeout
		}
		return "u-1", nil
	}}
	alice, unsubscribeAlice, err := broker.subscribe(0, false, false, server.eventFilter(&account.Identity{UserID: "u-1", Role: account.RoleUser}))
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeAlice()
	bob, unsubscribeBob, err := broker.subscribe(0, false, false, server.eventFilter(&account.Identity{UserID: "u-2", Role: account.RoleUser}))
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeBob()

	if err := broker.Emit(context.Background(), ipc.Event{Event: ipc.TopicConnectProgress, Payload: ipc.ConnectProgressEvent{AssetID: "asset-slow"}}); err != nil {
		t.Fatal(err)
	}
	received := drainReplay(t, alice, 1)
	if !strings.Contains(string(received[0].Payload), "asset-slow") {
		t.Fatalf("alice payload = %s, want the asset-slow event", received[0].Payload)
	}
	if got := len(bob.queue); got != 0 {
		t.Fatalf("bob received a foreign event: %d messages", got)
	}
}
