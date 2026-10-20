package terminal

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// recordEvents collects lifecycle notifications in arrival order.
type recordEvents struct {
	mu     sync.Mutex
	events []RecordingStatus
}

func (h *recordEvents) handler() func(RecordingStatus) {
	return func(status RecordingStatus) {
		h.mu.Lock()
		h.events = append(h.events, status)
		h.mu.Unlock()
	}
}

func (h *recordEvents) snapshot() []RecordingStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]RecordingStatus(nil), h.events...)
}

func requireEventOrder(t *testing.T, events []RecordingStatus, want ...RecordingStatus) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i, expected := range want {
		if events[i].State != expected.State || events[i].Generation != expected.Generation {
			t.Fatalf("event %d = %+v, want state %q generation %d (full stream %v)", i, events[i], expected.State, expected.Generation, events)
		}
	}
}

// P1: replacement start whose Stopped(old) handler reenters StopRecording.
// The nested stop's Stopped event must be queued behind the replacement's
// Running event, never ahead of it, and the final event must match the
// current state.
func TestRecordingReplacementStoppedCallbackReenterStopEventOrder(t *testing.T) {
	tab := newTestTab(t)
	path := filepath.Join(t.TempDir(), "replace-stop.log")
	collector := &recordEvents{}
	var reentered atomic.Bool
	tab.SetRecordingHandler(func(status RecordingStatus) {
		collector.handler()(status)
		if status.State == RecordingStopped && status.Generation == 1 && reentered.CompareAndSwap(false, true) {
			if _, err := tab.StopRecording(); err != nil {
				t.Errorf("reentrant stop = %v", err)
			}
		}
	})
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	requireEventOrder(t, collector.snapshot(),
		RecordingStatus{State: RecordingRunning, Generation: 1},
		RecordingStatus{State: RecordingStopped, Generation: 1},
		RecordingStatus{State: RecordingRunning, Generation: 2},
		RecordingStatus{State: RecordingStopped, Generation: 2},
	)
	if got := tab.RecordingStatus(); got.State != RecordingStopped || got.Generation != 2 {
		t.Fatalf("final status = %+v, want stopped generation 2", got)
	}
	if got := collector.snapshot(); len(got) > 0 {
		last := got[len(got)-1]
		if last.State != RecordingStopped || last.Generation != 2 {
			t.Fatalf("final event %+v contradicts state stopped(2)", last)
		}
	}
}

// P1: replacement start whose Stopped(old) handler reenters StartRecording.
// The nested start's events queue behind the replacement's Running event,
// and the final event must match the current state.
func TestRecordingReplacementStoppedCallbackReenterStartEventOrder(t *testing.T) {
	tab := newTestTab(t)
	path := filepath.Join(t.TempDir(), "replace-start.log")
	collector := &recordEvents{}
	var reentered atomic.Bool
	tab.SetRecordingHandler(func(status RecordingStatus) {
		collector.handler()(status)
		if status.State == RecordingStopped && status.Generation == 1 && reentered.CompareAndSwap(false, true) {
			if err := tab.StartRecording(path); err != nil {
				t.Errorf("reentrant start = %v", err)
			}
		}
	})
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	requireEventOrder(t, collector.snapshot(),
		RecordingStatus{State: RecordingRunning, Generation: 1},
		RecordingStatus{State: RecordingStopped, Generation: 1},
		RecordingStatus{State: RecordingRunning, Generation: 2},
		RecordingStatus{State: RecordingStopped, Generation: 2},
		RecordingStatus{State: RecordingRunning, Generation: 3},
	)
	if got := tab.RecordingStatus(); got.State != RecordingRunning || got.Generation != 3 {
		t.Fatalf("final status = %+v, want running generation 3", got)
	}
	if got := collector.snapshot(); len(got) > 0 {
		last := got[len(got)-1]
		if last.State != RecordingRunning || last.Generation != 3 {
			t.Fatalf("final event %+v contradicts state running(3)", last)
		}
	}
	if _, err := tab.StopRecording(); err != nil {
		t.Fatalf("final stop = %v", err)
	}
}
