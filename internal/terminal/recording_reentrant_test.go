package terminal

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// P1: a Stopped handler that synchronously reenters StopRecording must not
// deadlock the outer stop: the operation lock is released before handlers
// run, and the reentrant stop on an already-stopped tab is a no-op.
func TestRecordingStoppedCallbackCanReenterStop(t *testing.T) {
	tab := newTestTab(t)
	path := filepath.Join(t.TempDir(), "reenter-stop.log")
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	reentrant := make(chan error, 1)
	tab.SetRecordingHandler(func(status RecordingStatus) {
		if status.State != RecordingStopped {
			return
		}
		_, err := tab.StopRecording()
		reentrant <- err
	})
	stopped := make(chan error, 1)
	go func() {
		_, err := tab.StopRecording()
		stopped <- err
	}()
	select {
	case err := <-reentrant:
		if err != nil {
			t.Fatalf("reentrant stop = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stopped notification deadlocked reentering StopRecording")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("outer stop = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("outer StopRecording never returned")
	}
}

// P1: a Running handler that synchronously reenters StartRecording must not
// deadlock: the reentrant start serializes after the outer transition and
// replaces the generation without touching the operation lock twice.
func TestRecordingRunningCallbackCanReenterStart(t *testing.T) {
	tab := newTestTab(t)
	path := filepath.Join(t.TempDir(), "reenter-start.log")
	reentered := make(chan struct{})
	var reentering atomic.Bool
	tab.SetRecordingHandler(func(status RecordingStatus) {
		if status.State != RecordingRunning {
			return
		}
		// The reentrant start announces its own Running generation; only the
		// first announcement may reenter, and it must not block the nested
		// notification.
		if reentering.CompareAndSwap(false, true) {
			defer close(reentered)
			if err := tab.StartRecording(path); err != nil {
				t.Errorf("reentrant start = %v", err)
			}
		}
	})
	started := make(chan error, 1)
	go func() {
		started <- tab.StartRecording(path)
	}()
	select {
	case <-reentered:
	case <-time.After(2 * time.Second):
		t.Fatal("running notification deadlocked reentering StartRecording")
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("outer start = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("outer StartRecording never returned")
	}
	if got := tab.RecordingStatus(); got.State != RecordingRunning || got.Generation != 2 {
		t.Fatalf("status = %+v, want running generation 2", got)
	}
	if _, err := tab.StopRecording(); err != nil {
		t.Fatalf("final stop = %v", err)
	}
}

// P1: a Stopped handler that synchronously starts a new generation must not
// deadlock either.
func TestRecordingStoppedCallbackCanReenterStart(t *testing.T) {
	tab := newTestTab(t)
	path := filepath.Join(t.TempDir(), "reenter-stop-start.log")
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	reentrant := make(chan error, 1)
	var reentering atomic.Bool
	tab.SetRecordingHandler(func(status RecordingStatus) {
		if status.State != RecordingStopped {
			return
		}
		// Only the first Stopped announcement reenters; later stops (the
		// test's own final stop, tab cleanup) must not block on the channel.
		if reentering.CompareAndSwap(false, true) {
			reentrant <- tab.StartRecording(path)
		}
	})
	stopped := make(chan error, 1)
	go func() {
		_, err := tab.StopRecording()
		stopped <- err
	}()
	select {
	case err := <-reentrant:
		if err != nil {
			t.Fatalf("reentrant start = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stopped notification deadlocked reentering StartRecording")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("outer stop = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("outer StopRecording never returned")
	}
	if got := tab.RecordingStatus(); got.State != RecordingRunning || got.Generation != 2 {
		t.Fatalf("status = %+v, want running generation 2", got)
	}
	if _, err := tab.StopRecording(); err != nil {
		t.Fatalf("final stop = %v", err)
	}
}
