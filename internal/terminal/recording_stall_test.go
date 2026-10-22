package terminal

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// stuckRecordingWriter blocks every Write until gate closes and, unlike a
// real file, its Close does NOT unblock an in-flight Write — the deterministic
// counterexample for drain abandonment that must not join the writer.
type stuckRecordingWriter struct {
	gate      chan struct{}
	closed    chan struct{}
	closeOnce sync.Once

	mu         sync.Mutex
	closeCalls int
}

func newStuckRecordingWriter() *stuckRecordingWriter {
	return &stuckRecordingWriter{gate: make(chan struct{}), closed: make(chan struct{})}
}

func (w *stuckRecordingWriter) Write(p []byte) (int, error) {
	<-w.gate
	return len(p), nil
}

func (w *stuckRecordingWriter) Close() error {
	w.mu.Lock()
	w.closeCalls++
	w.mu.Unlock()
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}

func (w *stuckRecordingWriter) closes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closeCalls
}

// shortWriteOnCloseWriter parks its Write until Close, then reports a short
// count with an error, modeling an in-flight write interrupted by close.
type shortWriteOnCloseWriter struct {
	started   chan struct{}
	startOnce sync.Once
	closed    chan struct{}
	closeOnce sync.Once
	n         int
}

func (w *shortWriteOnCloseWriter) Write(p []byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	<-w.closed
	return w.n, errors.New("write interrupted by close")
}

func (w *shortWriteOnCloseWriter) Close() error {
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}

// P1: a writer that stays blocked even after Close must not turn the bounded
// drain abandonment into a permanent Stop hang, must not pause the output
// path behind the recording lock, and must not stall Tab.Close.
func TestRecordingStopDoesNotJoinStuckWriter(t *testing.T) {
	tab := newTestTab(t, WithRecordingDrainTimeout(50*time.Millisecond))
	o := newTestOutput(t, tab)
	writer := newStuckRecordingWriter()
	tab.rec = newRecording(tab, writer, "stuck.log", 1, defaultRecordingQueueChunks, defaultRecordingQueueBytes)
	if err := o.Chunk(t.Context(), []byte("blocked")); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() {
		_, err := tab.StopRecording()
		stopped <- err
	}()
	// The output path must keep flowing while the stop drain is in flight.
	chunkDone := make(chan error, 1)
	go func() {
		chunkDone <- o.Chunk(t.Context(), []byte("during-drain"))
	}()
	select {
	case err := <-chunkDone:
		if err != nil {
			t.Fatalf("Chunk during drain = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("output path blocked behind the recording drain")
	}
	select {
	case err := <-stopped:
		if !errors.Is(err, ErrRecordingDrainTimeout) {
			t.Fatalf("StopRecording = %v, want drain timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopRecording hung joining a writer that outlived its Close")
	}
	// Tab.Close must also complete promptly and must not join the writer.
	closed := make(chan struct{})
	go func() {
		tab.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Tab.Close hung on the stuck writer")
	}
	// The close is initiated asynchronously; wait for it to land.
	waitFor(t, "abandonment close", func() bool { return writer.closes() > 0 })
	close(writer.gate) // release the writer goroutine for test cleanliness
}

// P1: saturation must publish the failure transition promptly, off the
// interaction path, without waiting for the stalled writer to finish.
func TestRecordingSaturationEventDoesNotWaitForWriter(t *testing.T) {
	tab := newTestTab(t, WithRecordingQueue(2, 64))
	o := newTestOutput(t, tab)
	writer := newStuckRecordingWriter()
	tab.rec = newRecording(tab, writer, "saturated-stuck.log", 1, tab.recQueueChunks, tab.recQueueBytes)
	failures := make(chan RecordingStatus, 4)
	tab.SetRecordingHandler(func(status RecordingStatus) {
		if status.State == RecordingFailed {
			failures <- status
		}
	})
	ctx := t.Context()
	if err := o.Chunk(ctx, []byte("one")); err != nil {
		t.Fatal(err)
	}
	// Fill the queue behind the stuck first chunk until saturation.
	for _, chunk := range []string{"two", "three", "four"} {
		if err := o.Chunk(ctx, []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case event := <-failures:
		if event.SafeError != ErrRecordingSaturated.Error() || event.Dropped == 0 {
			t.Fatalf("failure event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("saturation failure event waited for the stalled writer")
	}
	select {
	case extra := <-failures:
		t.Fatalf("duplicate failure event: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
	close(writer.gate)
}

// P1: Tab.Close terminates a failed recording whose writer is still stuck:
// no goroutine or file leak, and the failure notification stays exactly
// once.
func TestRecordingTabCloseTerminatesFailedRecording(t *testing.T) {
	tab := NewTab("t1", "s1", 80, 24, UTF8, WithRecordingQueue(2, 64))
	o := newTestOutput(t, tab)
	writer := newStuckRecordingWriter()
	tab.rec = newRecording(tab, writer, "close-failed.log", 1, tab.recQueueChunks, tab.recQueueBytes)
	failures := make(chan RecordingStatus, 4)
	tab.SetRecordingHandler(func(status RecordingStatus) {
		if status.State == RecordingFailed {
			failures <- status
		}
	})
	ctx := t.Context()
	for _, chunk := range []string{"one", "two", "three", "four"} {
		if err := o.Chunk(ctx, []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	<-failures // the saturation announcement
	closed := make(chan struct{})
	go func() {
		tab.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Tab.Close hung on a failed recording with a stuck writer")
	}
	// The close is initiated asynchronously; wait for it to land.
	waitFor(t, "terminate close", func() bool { return writer.closes() > 0 })
	select {
	case extra := <-failures:
		t.Fatalf("failure announced more than once: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
	close(writer.gate)
}

// P2: an in-flight write interrupted by drain abandonment reports its
// unwritten remainder into Dropped even though the recording error was
// already set.
func TestRecordingDrainTimeoutDroppedAccounting(t *testing.T) {
	tab := newTestTab(t, WithRecordingDrainTimeout(50*time.Millisecond))
	o := newTestOutput(t, tab)
	writer := &shortWriteOnCloseWriter{started: make(chan struct{}), closed: make(chan struct{}), n: 2}
	tab.rec = newRecording(tab, writer, "short.log", 1, defaultRecordingQueueChunks, defaultRecordingQueueBytes)
	if err := o.Chunk(t.Context(), []byte("123456")); err != nil {
		t.Fatal(err)
	}
	<-writer.started
	n, err := tab.StopRecording()
	if !errors.Is(err, ErrRecordingDrainTimeout) {
		t.Fatalf("StopRecording = (%d, %v), want drain timeout", n, err)
	}
	// The interrupted write settles asynchronously: 2 of 6 bytes landed, the
	// remaining 4 must be counted as dropped.
	waitFor(t, "interrupted write accounting", func() bool {
		status := tab.RecordingStatus()
		return status.Bytes == 2 && status.Dropped == 4
	})
	status := tab.RecordingStatus()
	if status.State != RecordingFailed || !errors.Is(err, ErrRecordingDrainTimeout) {
		t.Fatalf("status = %+v, want failed with drain timeout", status)
	}
}
