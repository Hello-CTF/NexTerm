package terminal

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// parkRecordingWriter parks the recording writer on its wake channel: the
// first 1-byte chunk is written, then the test yields so the writer returns
// to the empty-queue park loop before the saturating chunk arrives.
func parkRecordingWriter(t *testing.T, o *Output, writer *fakeRecordingWriteCloser) {
	t.Helper()
	if err := o.Chunk(t.Context(), []byte("a")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first chunk written", func() bool {
		writes, _ := writer.counts()
		return writes == 1
	})
	runtime.Gosched() // let the writer reach the empty-queue park before saturating
}

// requireRecordingDone asserts the writer goroutine finalized within the
// timeout; a parked writer that is never woken fails here.
func requireRecordingDone(t *testing.T, rec *recording, what string) {
	t.Helper()
	select {
	case <-rec.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("recording writer never finalized: %s", what)
	}
}

// requireSingleClose asserts the file was closed exactly once.
func requireSingleClose(t *testing.T, writer *fakeRecordingWriteCloser) {
	t.Helper()
	waitFor(t, "asynchronous close", func() bool {
		_, closes := writer.counts()
		return closes == 1
	})
	if _, closes := writer.counts(); closes != 1 {
		t.Fatalf("file close calls = %d, want exactly 1", closes)
	}
}

// requireSingleSaturationFailure asserts exactly one Failed event with the
// saturation error and the expected dropped count, and no duplicate follows.
func requireSingleSaturationFailure(t *testing.T, failures chan RecordingStatus, generation, dropped uint64) {
	t.Helper()
	select {
	case event := <-failures:
		if event.State != RecordingFailed || event.Generation != generation ||
			event.SafeError != ErrRecordingSaturated.Error() || event.Dropped != dropped {
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
}

// P1: a single chunk larger than the byte budget saturates a parked writer;
// the writer must be woken and finalize on its own — done closes and the
// file is closed exactly once — without any Stop call.
func TestRecordingSaturationWakesParkedWriter(t *testing.T) {
	tab := newTestTab(t, WithRecordingQueue(1, 1))
	o := newTestOutput(t, tab)
	writer := &fakeRecordingWriteCloser{}
	rec := newRecording(tab, writer, "parked-saturated.log", 1, tab.recQueueChunks, tab.recQueueBytes)
	tab.rec = rec
	failures := make(chan RecordingStatus, 4)
	tab.SetRecordingHandler(func(status RecordingStatus) {
		if status.State == RecordingFailed {
			failures <- status
		}
	})
	parkRecordingWriter(t, o, writer)
	if err := o.Chunk(t.Context(), []byte("bc")); err != nil {
		t.Fatal(err)
	}
	status := tab.RecordingStatus()
	if status.State != RecordingFailed || status.SafeError != ErrRecordingSaturated.Error() || status.Dropped != 2 {
		t.Fatalf("status after byte saturation = %+v", status)
	}
	requireRecordingDone(t, rec, "byte-saturated parked writer was never woken")
	requireSingleClose(t, writer)
	requireSingleSaturationFailure(t, failures, 1, 2)
}

// P1: StopRecording after a byte saturation must wake the parked writer:
// done converges and the file closes exactly once.
func TestRecordingStopWakesParkedWriterAfterSaturation(t *testing.T) {
	tab := newTestTab(t, WithRecordingQueue(1, 1))
	o := newTestOutput(t, tab)
	writer := &fakeRecordingWriteCloser{}
	rec := newRecording(tab, writer, "parked-stop.log", 1, tab.recQueueChunks, tab.recQueueBytes)
	tab.rec = rec
	failures := make(chan RecordingStatus, 4)
	tab.SetRecordingHandler(func(status RecordingStatus) {
		if status.State == RecordingFailed {
			failures <- status
		}
	})
	parkRecordingWriter(t, o, writer)
	if err := o.Chunk(t.Context(), []byte("bc")); err != nil {
		t.Fatal(err)
	}
	if _, err := tab.StopRecording(); !errors.Is(err, ErrRecordingSaturated) {
		t.Fatalf("StopRecording = %v, want saturation", err)
	}
	requireRecordingDone(t, rec, "terminate after byte saturation never woke the writer")
	requireSingleClose(t, writer)
	requireSingleSaturationFailure(t, failures, 1, 2)
}

// P1: Tab.Close after a byte saturation must wake the parked writer: done
// converges and the file closes exactly once.
func TestRecordingTabCloseWakesParkedWriterAfterSaturation(t *testing.T) {
	tab := NewTab("t1", "s1", 80, 24, UTF8, WithRecordingQueue(1, 1))
	o := newTestOutput(t, tab)
	writer := &fakeRecordingWriteCloser{}
	rec := newRecording(tab, writer, "parked-close.log", 1, tab.recQueueChunks, tab.recQueueBytes)
	tab.rec = rec
	failures := make(chan RecordingStatus, 4)
	tab.SetRecordingHandler(func(status RecordingStatus) {
		if status.State == RecordingFailed {
			failures <- status
		}
	})
	parkRecordingWriter(t, o, writer)
	if err := o.Chunk(t.Context(), []byte("bc")); err != nil {
		t.Fatal(err)
	}
	tab.Close()
	requireRecordingDone(t, rec, "tab close after byte saturation never woke the writer")
	requireSingleClose(t, writer)
	requireSingleSaturationFailure(t, failures, 1, 2)
}

// A writer whose Write fails finalizes without any Stop: done closes, the
// file closes exactly once, and the failure event carries the write error
// and the dropped chunk.
func TestRecordingWriterFailureFinalizesWithoutStop(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	writeErr := errors.New("disk on fire")
	writer := &fakeRecordingWriteCloser{results: []recordingWriteResult{{err: writeErr}}}
	rec := newRecording(tab, writer, "write-failure.log", 1, tab.recQueueChunks, tab.recQueueBytes)
	tab.rec = rec
	failures := make(chan RecordingStatus, 4)
	tab.SetRecordingHandler(func(status RecordingStatus) {
		if status.State == RecordingFailed {
			failures <- status
		}
	})
	if err := o.Chunk(t.Context(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	requireRecordingDone(t, rec, "writer failure never finalized")
	requireSingleClose(t, writer)
	select {
	case event := <-failures:
		if event.SafeError != writeErr.Error() || event.Dropped != 1 || event.Generation != 1 {
			t.Fatalf("failure event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("writer failure never emitted a failure event")
	}
	select {
	case extra := <-failures:
		t.Fatalf("duplicate failure event: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

// gateRecordingFailure parks the asynchronous failure path of rec and
// signals entered once it is parked; the original behavior resumes when
// gate closes. Before the fix the parked section still owned the enqueue,
// so the failure landed only after the gate opened — the deterministic
// counterexample for the replacement-start reorder. After the fix the
// claim and the enqueue are one atomic step, so the gate only delays the
// drain and the enqueue order no longer depends on it.
func gateRecordingFailure(rec *recording, entered, gate chan struct{}) {
	orig := rec.onFailure
	rec.onFailure = func() {
		close(entered)
		<-gate
		orig()
	}
}

// P1: the saturated generation's failure must be enqueued before the
// replacement generation's Running status. The failure path is parked
// between claim and delivery while the replacement start publishes the new
// Running event; resuming the failure afterwards must not overtake it.
func TestRecordingSaturatedFailureEnqueuedBeforeReplacementRunning(t *testing.T) {
	tab := newTestTab(t, WithRecordingQueue(2, 64))
	o := newTestOutput(t, tab)
	writer := newGatedRecordingWriter()
	tab.rec = newRecording(tab, writer, "saturated-gen1.log", 1, tab.recQueueChunks, tab.recQueueBytes)
	tab.recGeneration = 1 // the injected generation 1; the replacement start becomes generation 2
	collector := &recordEvents{}
	tab.SetRecordingHandler(collector.handler())
	entered := make(chan struct{})
	gate := make(chan struct{})
	gateRecordingFailure(tab.rec, entered, gate)
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
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("saturated failure path never started")
	}
	if err := tab.StartRecording(filepath.Join(t.TempDir(), "gen2.log")); err != nil {
		t.Fatal(err)
	}
	// The replacement generation holds gen2.log open; stop it before the
	// test returns so the t.TempDir cleanup can remove the directory —
	// Windows cannot delete a file that is still open.
	defer tab.StopRecording()
	close(gate)
	close(writer.gate) // release the stuck writer for test cleanliness
	waitFor(t, "both lifecycle events", func() bool { return len(collector.snapshot()) == 2 })
	requireEventOrder(t, collector.snapshot(),
		RecordingStatus{State: RecordingFailed, Generation: 1},
		RecordingStatus{State: RecordingRunning, Generation: 2},
	)
}

// P1: a writer-side failure has the same ordering contract: the failed
// generation's failure must be enqueued before the replacement generation's
// Running status, even though the writer claims the notification itself.
func TestRecordingWriterFailureEnqueuedBeforeReplacementRunning(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	writeErr := errors.New("disk on fire")
	writer := &fakeRecordingWriteCloser{results: []recordingWriteResult{{err: writeErr}}}
	tab.rec = newRecording(tab, writer, "failed-gen1.log", 1, tab.recQueueChunks, tab.recQueueBytes)
	tab.recGeneration = 1 // the injected generation 1; the replacement start becomes generation 2
	collector := &recordEvents{}
	tab.SetRecordingHandler(collector.handler())
	entered := make(chan struct{})
	gate := make(chan struct{})
	gateRecordingFailure(tab.rec, entered, gate)
	if err := o.Chunk(t.Context(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer failure path never started")
	}
	if err := tab.StartRecording(filepath.Join(t.TempDir(), "gen2.log")); err != nil {
		t.Fatal(err)
	}
	// The replacement generation holds gen2.log open; stop it before the
	// test returns so the t.TempDir cleanup can remove the directory —
	// Windows cannot delete a file that is still open.
	defer tab.StopRecording()
	close(gate)
	waitFor(t, "both lifecycle events", func() bool { return len(collector.snapshot()) == 2 })
	requireEventOrder(t, collector.snapshot(),
		RecordingStatus{State: RecordingFailed, Generation: 1},
		RecordingStatus{State: RecordingRunning, Generation: 2},
	)
}
