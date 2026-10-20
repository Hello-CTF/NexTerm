package terminal

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// stuckCloseWriter writes fine but its Close blocks until gate closes: the
// deterministic counterexample for a file Close that never returns.
type stuckCloseWriter struct {
	gate      chan struct{}
	closeOnce sync.Once
	closed    chan struct{}

	mu         sync.Mutex
	closeCalls int
}

func newStuckCloseWriter() *stuckCloseWriter {
	return &stuckCloseWriter{gate: make(chan struct{}), closed: make(chan struct{})}
}

func (w *stuckCloseWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *stuckCloseWriter) Close() error {
	w.mu.Lock()
	w.closeCalls++
	w.mu.Unlock()
	w.closeOnce.Do(func() { close(w.closed) })
	<-w.gate
	return nil
}

func (w *stuckCloseWriter) closes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closeCalls
}

// P1: a file whose Close never returns must not let the writer hold the
// recording mutex forever: StopRecording's bounded abandonment, the output
// path, and Tab.Close all complete while the close is stuck.
func TestRecordingStopDoesNotBlockOnStuckClose(t *testing.T) {
	tab := newTestTab(t, WithRecordingDrainTimeout(50*time.Millisecond))
	o := newTestOutput(t, tab)
	writer := newStuckCloseWriter()
	tab.rec = newRecording(tab, writer, "stuck-close.log", 1, defaultRecordingQueueChunks, defaultRecordingQueueBytes)
	if err := o.Chunk(t.Context(), []byte("data")); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() {
		_, err := tab.StopRecording()
		stopped <- err
	}()
	// The output path must keep flowing while the stop is abandoning.
	chunkDone := make(chan error, 1)
	go func() {
		chunkDone <- o.Chunk(t.Context(), []byte("during-close"))
	}()
	select {
	case err := <-chunkDone:
		if err != nil {
			t.Fatalf("Chunk during stuck close = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("output path blocked behind a stuck file Close")
	}
	select {
	case err := <-stopped:
		if !errors.Is(err, ErrRecordingDrainTimeout) {
			t.Fatalf("StopRecording = %v, want drain timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopRecording hung on a stuck file Close")
	}
	// Tab.Close must also complete promptly.
	closed := make(chan struct{})
	go func() {
		tab.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Tab.Close hung on a stuck file Close")
	}
	// The close is initiated asynchronously; wait for it to land (it stays
	// stuck inside the fake until the gate opens).
	waitFor(t, "stuck close call", func() bool { return writer.closes() == 1 })
	close(writer.gate) // release the close goroutine for test cleanliness
}
