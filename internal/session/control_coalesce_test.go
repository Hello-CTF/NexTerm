package session

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type controlRecorder struct {
	mu     sync.Mutex
	events []ControlEvent
	wake   chan struct{}
}

func newControlRecorder() *controlRecorder { return &controlRecorder{wake: make(chan struct{}, 256)} }

func (r *controlRecorder) EmitSessionEvent(_ context.Context, event Event) error {
	if event.Topic != TopicTerminalControl {
		return nil
	}
	r.mu.Lock()
	r.events = append(r.events, event.Payload.(ControlEvent))
	r.mu.Unlock()
	r.wake <- struct{}{}
	return nil
}

func (r *controlRecorder) snapshot() []ControlEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ControlEvent(nil), r.events...)
}

func (r *controlRecorder) waitCount(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for len(r.snapshot()) < n {
		select {
		case <-r.wake:
		case <-deadline:
			t.Fatalf("got %d control events, want %d", len(r.snapshot()), n)
		}
	}
}

func withControlWindow(t *testing.T, d time.Duration) {
	old := controlCoalesceWindow
	controlCoalesceWindow = d
	t.Cleanup(func() { controlCoalesceWindow = old })
}

func newControlTestTab(t *testing.T, rec *controlRecorder) (*Manager, *Tab) {
	t.Helper()
	manager := NewManager(Config{Emitter: rec})
	t.Cleanup(func() { _ = manager.Close() })
	tab := &Tab{ID: "coalesce-" + t.Name(), session: &Session{}, subscribers: make(map[string]subscriber), terminal: &fakeTerminal{}}
	return manager, tab
}

func emitTitle(manager *Manager, tab *Tab, title string) {
	tab.mu.Lock()
	tab.title = title
	event := tab.controlEventLocked()
	tab.mu.Unlock()
	manager.emit(withCoalescibleControl(context.Background()), TopicTerminalControl, event)
}

func TestControlTitleBurstIsCoalescedAndClosingEventIsLast(t *testing.T) {
	withControlWindow(t, time.Hour)
	rec := newControlRecorder()
	manager, tab := newControlTestTab(t, rec)
	emitTitle(manager, tab, "initial")
	for i := 0; i < 50; i++ {
		emitTitle(manager, tab, fmt.Sprintf("title-%d", i))
	}
	tab.mu.Lock()
	tab.closed, tab.exited = true, true
	closing := tab.controlEventLocked()
	tab.mu.Unlock()
	manager.emit(context.Background(), TopicTerminalControl, closing)
	rec.waitCount(t, 3)
	events := rec.snapshot()
	if len(events) > 5 {
		t.Fatalf("51 title changes produced %d events", len(events))
	}
	if events[0].Title != "initial" {
		t.Fatalf("first title change must not be delayed, got %q", events[0].Title)
	}
	if got := events[len(events)-2]; got.Title != "title-49" || got.Exited {
		t.Fatalf("latest title must be delivered before the close event, got %+v", got)
	}
	if last := events[len(events)-1]; !last.Exited {
		t.Fatalf("close event must be last, got %+v", last)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Version <= events[i-1].Version {
			t.Fatalf("versions out of order: %+v", events)
		}
	}
}

func TestControlStateChangeFlushesHeldTitleInOrder(t *testing.T) {
	withControlWindow(t, time.Hour)
	rec := newControlRecorder()
	manager, tab := newControlTestTab(t, rec)
	emitTitle(manager, tab, "a")
	emitTitle(manager, tab, "b")
	emitTitle(manager, tab, "c")
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("held titles leaked: %d events", got)
	}
	tab.mu.Lock()
	tab.sizeOwner = "client-x"
	event := tab.controlEventLocked()
	tab.mu.Unlock()
	manager.emit(context.Background(), TopicTerminalControl, event)
	rec.waitCount(t, 3)
	events := rec.snapshot()
	if events[1].Title != "c" || events[1].SizeOwner != "" || events[2].SizeOwner != "client-x" || events[2].Title != "c" {
		t.Fatalf("size owner change must follow the held title, got %+v", events)
	}
}

func TestControlHeldTitleFlushesOnTrailingTimer(t *testing.T) {
	withControlWindow(t, 20*time.Millisecond)
	rec := newControlRecorder()
	manager, tab := newControlTestTab(t, rec)
	emitTitle(manager, tab, "a")
	emitTitle(manager, tab, "b")
	emitTitle(manager, tab, "c")
	rec.waitCount(t, 2)
	events := rec.snapshot()
	if len(events) != 2 || events[0].Title != "a" || events[1].Title != "c" {
		t.Fatalf("events = %+v, want leading a and trailing c", events)
	}
}

func TestControlIdleTitleChangeIsImmediate(t *testing.T) {
	withControlWindow(t, 20*time.Millisecond)
	rec := newControlRecorder()
	manager, tab := newControlTestTab(t, rec)
	emitTitle(manager, tab, "a")
	rec.waitCount(t, 1)
	time.Sleep(40 * time.Millisecond)
	emitTitle(manager, tab, "b")
	if got := rec.snapshot(); len(got) != 2 || got[1].Title != "b" {
		t.Fatalf("title after an idle window must be emitted synchronously, got %+v", got)
	}
}

func TestControlUnmarkedTitleChangeIsNeverDelayed(t *testing.T) {
	withControlWindow(t, time.Hour)
	rec := newControlRecorder()
	manager, tab := newControlTestTab(t, rec)
	emitTitle(manager, tab, "a")
	emitTitle(manager, tab, "b")
	tab.mu.Lock()
	tab.title = "renamed"
	event := tab.controlEventLocked()
	tab.mu.Unlock()
	manager.emit(context.Background(), TopicTerminalControl, event)
	rec.waitCount(t, 3)
	events := rec.snapshot()
	if events[1].Title != "b" || events[2].Title != "renamed" {
		t.Fatalf("explicit rename must flush the held title and follow it, got %+v", events)
	}
}
