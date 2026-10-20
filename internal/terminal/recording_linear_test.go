package terminal

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// gatedFailWriter parks its Write until gate closes, then fails it: the
// deterministic lever for a write failure landing while an outer stop waits
// on the recording drain.
type gatedFailWriter struct {
	started   chan struct{}
	startOnce sync.Once
	gate      chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	err       error
}

func newGatedFailWriter(err error) *gatedFailWriter {
	return &gatedFailWriter{started: make(chan struct{}), gate: make(chan struct{}), closed: make(chan struct{}), err: err}
}

func (w *gatedFailWriter) Write(p []byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	<-w.gate
	return 0, w.err
}

func (w *gatedFailWriter) Close() error {
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}

// P1: the Running status of a new generation must be enqueued inside the
// start migration section — before the new recording is visible to output
// and before a concurrent stop can enter. A stop racing the start must
// never publish Stopped(2) ahead of Running(2), and the final event must
// match the current state.
func TestRecordingConcurrentStopNeverOvertakesRunning(t *testing.T) {
	tab := newTestTab(t)
	collector := &recordEvents{}
	tab.SetRecordingHandler(collector.handler())
	path := filepath.Join(t.TempDir(), "gen.log")
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	gate := make(chan struct{})
	var gateOnce sync.Once
	release := func() { gateOnce.Do(func() { close(gate) }) }
	defer release()
	tab.beforeRunningEnqueue = func(*recording) {
		close(entered)
		<-gate
	}
	started := make(chan error, 1)
	go func() { started <- tab.StartRecording(path) }()
	<-entered
	stopDone := make(chan error, 1)
	go func() {
		_, err := tab.StopRecording()
		stopDone <- err
	}()
	// While the start migration is parked inside the operation lock, a
	// concurrent stop must not complete ahead of the Running event.
	select {
	case <-stopDone:
		t.Fatal("concurrent StopRecording completed inside the start migration section")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "all four events", func() bool { return len(collector.snapshot()) == 4 })
	requireEventOrder(t, collector.snapshot(),
		RecordingStatus{State: RecordingRunning, Generation: 1},
		RecordingStatus{State: RecordingStopped, Generation: 1},
		RecordingStatus{State: RecordingRunning, Generation: 2},
		RecordingStatus{State: RecordingStopped, Generation: 2},
	)
	if got := tab.RecordingStatus(); got.State != RecordingStopped || got.Generation != 2 {
		t.Fatalf("final status = %+v, want stopped generation 2", got)
	}
}

// P1: a same-generation writer failure must be enqueued after that
// generation's Running status. The migration section is paused with the new
// recording's file already closed; the first output chunk then fails the
// write. The failure must not reach the queue ahead of Running(1), and the
// final event must match the current state.
func TestRecordingSameGenerationFailureFollowsRunning(t *testing.T) {
	tab := newTestTab(t)
	collector := &recordEvents{}
	failed := make(chan RecordingStatus, 2)
	tab.SetRecordingHandler(func(status RecordingStatus) {
		collector.handler()(status)
		if status.State == RecordingFailed {
			failed <- status
		}
	})
	entered := make(chan struct{})
	gate := make(chan struct{})
	var gateOnce sync.Once
	release := func() { gateOnce.Do(func() { close(gate) }) }
	defer release()
	tab.beforeRunningEnqueue = func(rec *recording) {
		// Fail the generation's file before its Running status publishes.
		if err := rec.file.Close(); err != nil {
			t.Errorf("close new recording file: %v", err)
		}
		close(entered)
		<-gate
	}
	started := make(chan error, 1)
	go func() { started <- tab.StartRecording(filepath.Join(t.TempDir(), "gen1.log")) }()
	<-entered
	o := newTestOutput(t, tab)
	chunkDone := make(chan error, 1)
	go func() { chunkDone <- o.Chunk(t.Context(), []byte("x")) }()
	// On the unfixed code the failure lands inside the window and the
	// counterexample fails at the order assertion below; on the fixed code
	// the output path is still locked out and the window simply elapses.
	select {
	case <-failed:
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if err := <-chunkDone; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both lifecycle events", func() bool { return len(collector.snapshot()) == 2 })
	requireEventOrder(t, collector.snapshot(),
		RecordingStatus{State: RecordingRunning, Generation: 1},
		RecordingStatus{State: RecordingFailed, Generation: 1},
	)
	if got := tab.RecordingStatus(); got.State != RecordingFailed || got.Generation != 1 {
		t.Fatalf("final status = %+v, want failed generation 1", got)
	}
}

// P2: an outer stop waiting on the drain must not wait for the failure
// handler: the writer enqueues the failure and finalizes done before the
// asynchronous drain runs handlers, so a handler that synchronously
// reenters StopRecording cannot form a lock cycle with the outer stop.
func TestRecordingWriterFailureDonePrecedesHandlerDrain(t *testing.T) {
	tab := newTestTab(t, WithRecordingDrainTimeout(50*time.Millisecond))
	o := newTestOutput(t, tab)
	writeErr := errors.New("disk on fire")
	writer := newGatedFailWriter(writeErr)
	rec := newRecording(tab, writer, "gen1.log", 1, tab.recQueueChunks, tab.recQueueBytes)
	tab.rec = rec
	tab.recGeneration = 1
	doneClosedBeforeHandler := make(chan bool, 1)
	reentrant := make(chan error, 1)
	tab.SetRecordingHandler(func(status RecordingStatus) {
		if status.State != RecordingFailed {
			return
		}
		select {
		case <-rec.done:
			doneClosedBeforeHandler <- true
		default:
			doneClosedBeforeHandler <- false
		}
		_, err := tab.StopRecording() // synchronous reentry
		reentrant <- err
	})
	if err := o.Chunk(t.Context(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	<-writer.started
	stopDone := make(chan error, 1)
	go func() {
		_, err := tab.StopRecording()
		stopDone <- err
	}()
	// The outer stop now holds the operation lock and waits on done; the
	// in-flight write fails from here on.
	waitFor(t, "outer stop drain", func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return rec.closed
	})
	close(writer.gate)
	select {
	case err := <-stopDone:
		if !errors.Is(err, writeErr) || errors.Is(err, ErrRecordingDrainTimeout) {
			t.Fatalf("outer StopRecording = %v, want the write failure without a drain timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("outer StopRecording never returned")
	}
	select {
	case err := <-reentrant:
		if !errors.Is(err, writeErr) {
			t.Fatalf("reentrant stop = %v, want the write failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reentrant failure handler never completed")
	}
	if !<-doneClosedBeforeHandler {
		t.Fatal("failure handler ran before the recording was finalized")
	}
}
