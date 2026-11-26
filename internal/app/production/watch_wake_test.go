package production

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/watches"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type wakeExecutor struct {
	mu       sync.Mutex
	triggers []cron.Trigger
	results  chan error
	calls    chan cron.Trigger
}

func (e *wakeExecutor) Execute(_ context.Context, trigger cron.Trigger) error {
	e.mu.Lock()
	e.triggers = append(e.triggers, trigger)
	e.mu.Unlock()
	e.calls <- trigger
	return <-e.results
}

type wakeHarness struct {
	waker    *eventWaker
	executor *wakeExecutor
	clock    time.Time
	timers   chan chan time.Time
	notices  chan string
	advance  func(time.Duration)
}

func newWakeHarness(t *testing.T) *wakeHarness {
	h := &wakeHarness{
		executor: &wakeExecutor{results: make(chan error), calls: make(chan cron.Trigger, 4)},
		clock:    time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC),
		timers:   make(chan chan time.Time, 4),
		notices:  make(chan string, 4),
	}
	var mu sync.Mutex
	h.waker = newEventWaker(h.executor, func(_ context.Context, text string) { h.notices <- text }, nil)
	h.waker.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return h.clock }
	h.waker.after = func(time.Duration) <-chan time.Time {
		fire := make(chan time.Time, 1)
		h.timers <- fire
		return fire
	}
	advance := func(d time.Duration) { mu.Lock(); h.clock = h.clock.Add(d); mu.Unlock() }
	h.advance = advance
	t.Cleanup(h.waker.Close)
	return h
}

func (h *wakeHarness) event(id string) watches.Event {
	return watches.Event{DeliveryID: id, WatchID: "watch-1", FiredAt: h.waker.now().UnixMilli()}
}

func (h *wakeHarness) waitCall(t *testing.T) cron.Trigger {
	t.Helper()
	select {
	case trigger := <-h.executor.calls:
		return trigger
	case <-time.After(5 * time.Second):
		t.Fatal("no run started")
		return cron.Trigger{}
	}
}

func (h *wakeHarness) waitIdle(t *testing.T, conversation string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.waker.mu.Lock()
		waiting := h.waker.waiting[conversation]
		h.waker.mu.Unlock()
		if !waiting {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("wake did not finish")
}

func TestEventWakerStartsAnEventRunUnderTheOwner(t *testing.T) {
	h := newWakeHarness(t)
	ctx := ipc.WithUserID(context.Background(), "user-1")
	h.waker.Wake(ctx, "conv-1", h.event("d1"))
	trigger := h.waitCall(t)
	if trigger.SessionID != "conv-1" || trigger.Source != agent.SourceEvent || trigger.OwnerID != "user-1" || trigger.OwnerKind != cron.OwnerKindAccount {
		t.Fatalf("trigger = %+v", trigger)
	}
	if trigger.Prompt != eventWakePrompt {
		t.Fatal("the run message must be the fixed trigger note, never event text")
	}
	h.executor.results <- nil
	h.waitIdle(t, "conv-1")
}

func TestEventWakerLimitsRate(t *testing.T) {
	h := newWakeHarness(t)
	ctx := context.Background()
	h.waker.Wake(ctx, "conv-1", h.event("d1"))
	h.waitCall(t)
	// A second event while the first wake runs is folded into it.
	h.waker.Wake(ctx, "conv-1", h.event("d2"))
	h.executor.results <- nil
	h.waitIdle(t, "conv-1")
	// Within the minimum gap nothing starts.
	h.advance(eventWakeMinGap / 2)
	h.waker.Wake(ctx, "conv-1", h.event("d3"))
	// An event older than the age limit never wakes.
	stale := h.event("d4")
	h.advance(eventWakeMinGap)
	stale.FiredAt = h.waker.now().Add(-eventWakeMaxAge - time.Second).UnixMilli()
	h.waker.Wake(ctx, "conv-1", stale)
	select {
	case trigger := <-h.executor.calls:
		t.Fatalf("unexpected run %+v", trigger)
	default:
	}
	// The hourly cap holds even when every wake respects the gap.
	for i := 1; i < eventWakePerHour; i++ {
		h.waker.Wake(ctx, "conv-1", h.event("ok"))
		h.waitCall(t)
		h.executor.results <- nil
		h.waitIdle(t, "conv-1")
		h.advance(eventWakeMinGap)
	}
	h.waker.Wake(ctx, "conv-1", h.event("over"))
	select {
	case trigger := <-h.executor.calls:
		t.Fatalf("wake past the hourly cap: %+v", trigger)
	default:
	}
}

func TestEventWakerRetriesABusyConversationAndReportsAStop(t *testing.T) {
	h := newWakeHarness(t)
	h.waker.Wake(context.Background(), "conv-1", h.event("d1"))
	h.waitCall(t)
	h.executor.results <- cron.ErrRunSkipped
	timer := <-h.timers
	h.advance(eventWakeBusyRetry)
	timer <- h.waker.now()
	h.waitCall(t)
	h.executor.results <- errors.New("stopped at confirm")
	select {
	case notice := <-h.notices:
		if notice == "" {
			t.Fatal("empty notice")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stopped event run was not reported")
	}
	h.waitIdle(t, "conv-1")
}
