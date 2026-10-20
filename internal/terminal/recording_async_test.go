package terminal

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"
)

// gatedRecordingWriter parks every Write until gate is closed (or the writer
// is closed), letting a test hold the recording writer mid-write.
// writeStarted is closed when the first Write call is entered.
type gatedRecordingWriter struct {
	mu           sync.Mutex
	writes       [][]byte
	gate         chan struct{}
	closed       chan struct{}
	closeOnce    sync.Once
	writeStarted chan struct{}
	startedOnce  sync.Once
}

func newGatedRecordingWriter() *gatedRecordingWriter {
	return &gatedRecordingWriter{gate: make(chan struct{}), closed: make(chan struct{}), writeStarted: make(chan struct{})}
}

func (w *gatedRecordingWriter) Write(p []byte) (int, error) {
	w.startedOnce.Do(func() { close(w.writeStarted) })
	select {
	case <-w.gate:
	case <-w.closed:
		return 0, errors.New("write on closed recording")
	}
	w.mu.Lock()
	w.writes = append(w.writes, append([]byte(nil), p...))
	w.mu.Unlock()
	return len(p), nil
}

func (w *gatedRecordingWriter) Close() error {
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}

func (w *gatedRecordingWriter) written() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []byte
	for _, chunk := range w.writes {
		out = append(out, chunk...)
	}
	return out
}

// T-06: terminal interaction must never block on a stalled recording file:
// Chunk only enqueues, and the queued bytes are written once the file
// recovers.
func TestRecordingChunkDoesNotBlockOnStalledWriter(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	writer := newGatedRecordingWriter()
	tab.rec = newRecording(tab, writer, "stalled.log", 1, defaultRecordingQueueChunks, defaultRecordingQueueBytes)
	done := make(chan error, 1)
	go func() {
		done <- o.Chunk(t.Context(), []byte("interactive"))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Chunk = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Chunk blocked on a stalled recording write")
	}
	close(writer.gate)
	n, err := tab.StopRecording()
	if err != nil || n != uint64(len("interactive")) {
		t.Fatalf("StopRecording = (%d, %v), want (%d, nil)", n, err, len("interactive"))
	}
	if got := writer.written(); !bytes.Equal(got, []byte("interactive")) {
		t.Fatalf("recorded = %q", got)
	}
}

// T-06: a saturated recording queue degrades visibly — the status turns
// failed with a saturation error and a dropped count, the failure event fires
// exactly once, and the already-queued chunks still drain.
func TestRecordingSaturationFailsVisibly(t *testing.T) {
	tab := newTestTab(t, WithRecordingQueue(2, 64))
	o := newTestOutput(t, tab)
	writer := newGatedRecordingWriter()
	tab.rec = newRecording(tab, writer, "saturated.log", 1, tab.recQueueChunks, tab.recQueueBytes)
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
	// Hold the first chunk in flight so the queue deterministically fills.
	select {
	case <-writer.writeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("recording writer never started the first chunk")
	}
	for _, chunk := range []string{"two", "three", "four"} {
		if err := o.Chunk(ctx, []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	status := tab.RecordingStatus()
	if status.State != RecordingFailed || status.SafeError != ErrRecordingSaturated.Error() {
		t.Fatalf("status = %+v, want failed with saturation", status)
	}
	if status.Dropped == 0 {
		t.Fatalf("saturation left no visible dropped count: %+v", status)
	}
	close(writer.gate)
	select {
	case event := <-failures:
		if event.Generation != 1 || event.Dropped == 0 {
			t.Fatalf("failure event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("saturation never emitted a failure event")
	}
	select {
	case extra := <-failures:
		t.Fatalf("duplicate failure event: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
	if got := writer.written(); !bytes.Equal(got, []byte("onetwothree")) {
		t.Fatalf("drained recording = %q, want the queued prefix without the dropped chunk", got)
	}
}

// T-06: StopRecording drains the queued chunks in FIFO order before closing.
func TestRecordingStopDrainsQueuedChunksInOrder(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	writer := newGatedRecordingWriter()
	tab.rec = newRecording(tab, writer, "drain.log", 1, defaultRecordingQueueChunks, defaultRecordingQueueBytes)
	ctx := t.Context()
	if err := o.Chunk(ctx, []byte("a")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writer.writeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("recording writer never started the first chunk")
	}
	for _, chunk := range []string{"b", "c"} {
		if err := o.Chunk(ctx, []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	stopped := make(chan error, 1)
	go func() {
		_, err := tab.StopRecording()
		stopped <- err
	}()
	select {
	case err := <-stopped:
		t.Fatalf("StopRecording returned before the queue drained: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(writer.gate)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("StopRecording = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StopRecording never finished the drain")
	}
	if got := writer.written(); !bytes.Equal(got, []byte("abc")) {
		t.Fatalf("recorded = %q, want FIFO abc", got)
	}
	if n := tab.RecordingBytes(); n != 0 {
		t.Fatalf("bytes after clean stop = %d, want 0", n)
	}
}

// T-06: a drain that cannot finish within the timeout is abandoned
// explicitly: StopRecording reports the timeout, the queued bytes are
// dropped, and the failure stays queryable.
func TestRecordingStopDrainTimeoutAbandonsQueue(t *testing.T) {
	tab := newTestTab(t, WithRecordingDrainTimeout(50*time.Millisecond))
	o := newTestOutput(t, tab)
	writer := newGatedRecordingWriter()
	tab.rec = newRecording(tab, writer, "abandon.log", 1, defaultRecordingQueueChunks, defaultRecordingQueueBytes)
	if err := o.Chunk(t.Context(), []byte("stuck")); err != nil {
		t.Fatal(err)
	}
	n, err := tab.StopRecording()
	if !errors.Is(err, ErrRecordingDrainTimeout) {
		t.Fatalf("StopRecording = (%d, %v), want drain timeout", n, err)
	}
	if n != 0 {
		t.Fatalf("abandoned drain reported %d bytes, want 0", n)
	}
	status := tab.RecordingStatus()
	if status.State != RecordingFailed {
		t.Fatalf("status = %+v, want failed with drain timeout", status)
	}
	if _, err := tab.StopRecording(); !errors.Is(err, ErrRecordingDrainTimeout) {
		t.Fatalf("repeated StopRecording = %v, want retained drain timeout", err)
	}
	close(writer.gate)
}

// T-06: closing the tab stops an active recording instead of leaking the
// writer goroutine and the file.
func TestRecordingCloseStopsActiveRecording(t *testing.T) {
	tab := NewTab("t1", "s1", 80, 24, UTF8)
	writer := &fakeRecordingWriteCloser{}
	tab.rec = newRecording(tab, writer, "close.log", 1, defaultRecordingQueueChunks, defaultRecordingQueueBytes)
	tab.Close()
	if _, closes := writer.counts(); closes != 1 {
		t.Fatalf("writer close calls = %d, want 1", closes)
	}
	if got := tab.RecordingStatus(); got.State != RecordingStopped {
		t.Fatalf("status after tab close = %+v, want stopped", got)
	}
	tab.Close()
	if _, closes := writer.counts(); closes != 1 {
		t.Fatalf("repeated close closed the writer again: %d", closes)
	}
}
