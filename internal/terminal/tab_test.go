package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestScreenTextHasNoANSIResidue(t *testing.T) {
	tab := newTestTab(t)
	tab.Feed([]byte("\x1b[2J\x1b[H"))
	tab.Feed([]byte("hello \x1b[31;1mworld\x1b[0m\r\n"))
	tab.Feed([]byte("$ \x1b[?25lhidden"))
	text := tab.ScreenText()
	if !strings.Contains(text, "hello world") {
		t.Fatalf("text = %q", text)
	}
	if !strings.Contains(text, "$") {
		t.Fatalf("prompt missing: %q", text)
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("ANSI residue: %q", text)
	}
	if tab.State().CursorVisible {
		t.Fatal("?25l should hide cursor in State")
	}
}

func TestCursorPositionReported(t *testing.T) {
	tab := newTestTab(t)
	tab.Feed([]byte("abc"))
	snap := tab.Snapshot()
	if snap.Rows != 24 || snap.Cols != 80 || snap.CursorRow != 0 || snap.CursorCol != 3 {
		t.Fatalf("snap = %+v", snap)
	}
	if len(snap.Lines) == 0 || snap.Lines[0] != "abc" {
		t.Fatalf("lines = %q", snap.Lines)
	}
}

func TestAltScreenDetection(t *testing.T) {
	tab := newTestTab(t)
	tab.Feed([]byte("\x1b[?1049h"))
	if !tab.Snapshot().AltScreen || !tab.State().AltScreen {
		t.Fatal("expected alt screen")
	}
	tab.Feed([]byte("\x1b[?1049l"))
	if tab.Snapshot().AltScreen {
		t.Fatal("expected main screen")
	}
}

func TestIdleDetection(t *testing.T) {
	clock := newFakeClock()
	tab := newTestTab(t, WithClock(clock.now))
	tab.Feed([]byte("boom\r\n"))
	if tab.IsIdle(5 * time.Second) {
		t.Fatal("just fed: not idle")
	}
	clock.advance(10 * time.Millisecond)
	if !tab.IsIdle(time.Millisecond) {
		t.Fatal("should be idle at 1ms quiet")
	}
	if got := tab.LastOutputAgo(); got != 10*time.Millisecond {
		t.Fatalf("ago = %v", got)
	}
	if tab.Snapshot().LastOutputMsAgo != 10 {
		t.Fatalf("snapshot ago = %d", tab.Snapshot().LastOutputMsAgo)
	}
}

func TestTailLinesWithScrollback(t *testing.T) {
	tab := newTestTab(t)
	for i := 0; i < 100; i++ {
		tab.Feed([]byte(fmt.Sprintf("line-%d\r\n", i)))
	}
	tail := tab.TailLines(10)
	if len(tail) != 10 {
		t.Fatalf("tail len = %d: %q", len(tail), tail)
	}
	found := false
	for _, l := range tail {
		if strings.Contains(l, "line-99") {
			found = true
		}
	}
	if !found {
		t.Fatalf("tail missing line-99: %q", tail)
	}
}

func TestTailLinesDecodesCurrentEncoding(t *testing.T) {
	tab := NewTab("t", "s", 80, 24, GBK)
	t.Cleanup(tab.Close)
	raw := encodeForTest(t, simplifiedchinese.GBK, "第一行\r\n第二行\r\n")
	tab.Feed(raw)
	tail := tab.TailLines(30)
	joined := strings.Join(tail, "\n")
	if !strings.Contains(joined, "第一行") || !strings.Contains(joined, "第二行") {
		t.Fatalf("GBK tail garbled: %q", tail)
	}
}

func TestResizeUpdatesStateMachine(t *testing.T) {
	tab := newTestTab(t)
	var got [2]int
	tab.SetResizeHandler(func(cols, rows int) { got = [2]int{cols, rows} })
	if err := tab.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	tab.Feed([]byte("x"))
	snap := tab.Snapshot()
	if snap.Cols != 120 || snap.Rows != 40 || tab.Cols() != 120 || tab.Rows() != 40 {
		t.Fatalf("snap = %+v", snap)
	}
	if got != [2]int{120, 40} {
		t.Fatalf("resize handler got %v", got)
	}
	for _, dims := range [][2]int{{0, 24}, {80, 0}, {1025, 24}, {80, 1025}, {-1, 5}} {
		if err := tab.Resize(dims[0], dims[1]); err == nil {
			t.Fatalf("resize %v should fail", dims)
		}
	}
}

func TestRecordingLifecycle(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	path := filepath.Join(t.TempDir(), "rec.log")
	if err := os.WriteFile(path, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := o.Chunk(ctx, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := o.Chunk(ctx, []byte("def")); err != nil {
		t.Fatal(err)
	}
	if tab.RecordingBytes() != 6 {
		t.Fatalf("recorded = %d", tab.RecordingBytes())
	}
	if n, err := tab.StopRecording(); n != 6 || err != nil {
		t.Fatalf("stop = (%d, %v)", n, err)
	}
	if n, err := tab.StopRecording(); n != 0 || err != nil {
		t.Fatalf("second stop = (%d, %v), want (0, nil)", n, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("OLDabcdef")) {
		t.Fatalf("file = %q", data)
	}
	tab.Feed([]byte("zzz"))
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	if tab.RecordingBytes() != 0 {
		t.Fatal("counter should reset")
	}
	if err := o.Chunk(ctx, []byte("g")); err != nil {
		t.Fatal(err)
	}
	if n, err := tab.StopRecording(); n != 1 || err != nil {
		t.Fatalf("stop = (%d, %v)", n, err)
	}
	data, _ = os.ReadFile(path)
	if !bytes.Equal(data, []byte("OLDabcdefg")) {
		t.Fatalf("file = %q", data)
	}
}

func TestRecordingFailureRemainsQueryable(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	path := filepath.Join(t.TempDir(), "failure.log")
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	if err := tab.rec.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := o.Chunk(context.Background(), []byte("after-close")); err != nil {
		t.Fatal(err)
	}
	recorded, stopErr := tab.StopRecording()
	if recorded != 0 || !errors.Is(stopErr, os.ErrClosed) {
		t.Fatalf("stop = (%d, %v), want closed file error", recorded, stopErr)
	}
	recorded, stopErr = tab.StopRecording()
	if recorded != 0 || !errors.Is(stopErr, os.ErrClosed) {
		t.Fatalf("repeated stop = (%d, %v), want retained closed file error", recorded, stopErr)
	}
}

func TestRecordingShortWriteStopsRecordingWithoutStoppingOutput(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	writer := &fakeRecordingWriteCloser{results: []recordingWriteResult{{n: 2}}}
	tab.rec = &recording{file: writer}
	sink := &memSink{}
	o.Attach("sink", sink)
	if err := o.Chunk(context.Background(), []byte("abcd")); err != nil {
		t.Fatal(err)
	}
	if err := o.Chunk(context.Background(), []byte("ef")); err != nil {
		t.Fatal(err)
	}
	recorded, stopErr := tab.StopRecording()
	if recorded != 2 || !errors.Is(stopErr, io.ErrShortWrite) {
		t.Fatalf("stop = (%d, %v), want (2, io.ErrShortWrite)", recorded, stopErr)
	}
	recorded, stopErr = tab.StopRecording()
	if recorded != 2 || !errors.Is(stopErr, io.ErrShortWrite) {
		t.Fatalf("repeated stop = (%d, %v), want retained short write", recorded, stopErr)
	}
	if writes, closes := writer.counts(); writes != 1 || closes != 1 {
		t.Fatalf("write/close calls = %d/%d, want 1/1", writes, closes)
	}
	if got := sink.Bytes(); !bytes.Equal(got, []byte("abcdef")) {
		t.Fatalf("terminal output = %q, want %q", got, "abcdef")
	}
}

func TestRecordingWriteFailureKeepsActualBytesAndFirstError(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	writeErr := errors.New("write failed")
	closeErr := errors.New("close failed")
	writer := &fakeRecordingWriteCloser{
		results:  []recordingWriteResult{{n: 3}, {n: 2, err: writeErr}, {n: 7}},
		closeErr: closeErr,
	}
	tab.rec = &recording{file: writer}
	for _, chunk := range [][]byte{[]byte("abc"), []byte("defgh"), []byte("ignored")} {
		if err := o.Chunk(context.Background(), chunk); err != nil {
			t.Fatal(err)
		}
	}
	recorded, stopErr := tab.StopRecording()
	if recorded != 5 || !errors.Is(stopErr, writeErr) {
		t.Fatalf("stop = (%d, %v), want (5, write failure)", recorded, stopErr)
	}
	if errors.Is(stopErr, closeErr) {
		t.Fatalf("close error replaced first write error: %v", stopErr)
	}
	if writes, closes := writer.counts(); writes != 2 || closes != 1 {
		t.Fatalf("write/close calls = %d/%d, want 2/1", writes, closes)
	}
	if got := tab.Dump(0); !bytes.Equal(got, []byte("abcdefghignored")) {
		t.Fatalf("terminal dump = %q", got)
	}
}

func TestRecordingCloseFailureIsIdempotentAndStopsWrites(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	closeErr := errors.New("close failed")
	writer := &fakeRecordingWriteCloser{closeErr: closeErr}
	tab.rec = &recording{file: writer}
	if err := o.Chunk(context.Background(), []byte("abc")); err != nil {
		t.Fatal(err)
	}
	recorded, stopErr := tab.StopRecording()
	if recorded != 3 || !errors.Is(stopErr, closeErr) {
		t.Fatalf("stop = (%d, %v), want (3, close failure)", recorded, stopErr)
	}
	if err := o.Chunk(context.Background(), []byte("after")); err != nil {
		t.Fatal(err)
	}
	recorded, stopErr = tab.StopRecording()
	if recorded != 3 || !errors.Is(stopErr, closeErr) {
		t.Fatalf("repeated stop = (%d, %v), want retained close failure", recorded, stopErr)
	}
	if writes, closes := writer.counts(); writes != 1 || closes != 1 {
		t.Fatalf("write/close calls = %d/%d, want 1/1", writes, closes)
	}
	if got := tab.RecordingBytes(); got != 3 {
		t.Fatalf("queryable recorded bytes = %d, want 3", got)
	}
}

func TestRecordingStartAfterFailureClearsOldGeneration(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	writeErr := errors.New("old generation failed")
	oldWriter := &fakeRecordingWriteCloser{results: []recordingWriteResult{{err: writeErr}}}
	tab.rec = &recording{file: oldWriter}
	if err := o.Chunk(context.Background(), []byte("old")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "new-generation.log")
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	if got := tab.RecordingBytes(); got != 0 {
		t.Fatalf("new generation bytes = %d, want 0", got)
	}
	if err := o.Chunk(context.Background(), []byte("new")); err != nil {
		t.Fatal(err)
	}
	recorded, stopErr := tab.StopRecording()
	if recorded != 3 || stopErr != nil {
		t.Fatalf("new generation stop = (%d, %v), want (3, nil)", recorded, stopErr)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, []byte("new")) {
		t.Fatalf("new generation file = %q, err = %v", data, err)
	}
	if writes, closes := oldWriter.counts(); writes != 1 || closes != 1 {
		t.Fatalf("old generation write/close calls = %d/%d, want 1/1", writes, closes)
	}
}

func TestRecordingStartDoesNotHidePreviousCloseFailure(t *testing.T) {
	tab := newTestTab(t)
	closeErr := errors.New("previous close failed")
	oldWriter := &fakeRecordingWriteCloser{closeErr: closeErr}
	tab.rec = &recording{file: oldWriter}
	path := filepath.Join(t.TempDir(), "replacement.log")
	if err := tab.StartRecording(path); !errors.Is(err, closeErr) {
		t.Fatalf("start error = %v, want previous close failure", err)
	}
	if tab.rec.file != oldWriter {
		t.Fatal("failed start replaced the previous recording")
	}
	if err := tab.StartRecording(path); err != nil {
		t.Fatalf("explicit restart after visible failure: %v", err)
	}
	if recorded, err := tab.StopRecording(); recorded != 0 || err != nil {
		t.Fatalf("replacement stop = (%d, %v), want (0, nil)", recorded, err)
	}
	if _, closes := oldWriter.counts(); closes != 1 {
		t.Fatalf("old writer close calls = %d, want 1", closes)
	}
}

func TestRecordingStatusLifecycle(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	if got := tab.RecordingStatus(); got.State != RecordingNeverStarted || got.Generation != 0 {
		t.Fatalf("initial status = %+v", got)
	}
	path := filepath.Join(t.TempDir(), "status.log")
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	if got := tab.RecordingStatus(); got.State != RecordingRunning || got.Generation != 1 || got.Path != path || got.Bytes != 0 {
		t.Fatalf("running status = %+v", got)
	}
	if err := o.Chunk(context.Background(), []byte("abc")); err != nil {
		t.Fatal(err)
	}
	if recorded, err := tab.StopRecording(); recorded != 3 || err != nil {
		t.Fatalf("stop = (%d, %v)", recorded, err)
	}
	if got := tab.RecordingStatus(); got.State != RecordingStopped || got.Generation != 1 || got.Path != path || got.Bytes != 3 || got.SafeError != "" {
		t.Fatalf("stopped status = %+v", got)
	}
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	if got := tab.RecordingStatus(); got.State != RecordingRunning || got.Generation != 2 || got.Bytes != 0 || got.SafeError != "" {
		t.Fatalf("restarted status = %+v", got)
	}
	if recorded, err := tab.StopRecording(); recorded != 0 || err != nil {
		t.Fatalf("restart stop = (%d, %v), want (0, nil)", recorded, err)
	}
}

func TestRecordingFailureEventOnceAndObserverCanStop(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	path := filepath.Join(t.TempDir(), "event.log")
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("injected write failure")
	writer := &fakeRecordingWriteCloser{results: []recordingWriteResult{{err: writeErr}}}
	if err := tab.rec.file.Close(); err != nil {
		t.Fatal(err)
	}
	tab.rec.file = writer
	failures := 0
	reentrant := make(chan error, 1)
	tab.SetRecordingHandler(func(status RecordingStatus) {
		if status.State != RecordingFailed {
			return
		}
		failures++
		_, err := tab.StopRecording()
		reentrant <- err
	})
	if err := o.Chunk(context.Background(), []byte("first")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-reentrant:
		if !errors.Is(err, writeErr) {
			t.Fatalf("reentrant stop error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recording observer deadlocked reentering StopRecording")
	}
	if err := o.Chunk(context.Background(), []byte("second")); err != nil {
		t.Fatal(err)
	}
	if _, err := tab.StopRecording(); !errors.Is(err, writeErr) {
		t.Fatalf("stop error = %v", err)
	}
	if failures != 1 {
		t.Fatalf("failure notifications = %d, want 1", failures)
	}
	if got := tab.RecordingStatus(); got.State != RecordingFailed || got.SafeError != writeErr.Error() || got.Path != path {
		t.Fatalf("failed status = %+v", got)
	}
}

func TestRecordingEventGenerations(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	var events []RecordingStatus
	tab.SetRecordingHandler(func(status RecordingStatus) {
		events = append(events, status)
	})
	path := filepath.Join(t.TempDir(), "generations.log")
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("generation one failed")
	if err := tab.rec.file.Close(); err != nil {
		t.Fatal(err)
	}
	tab.rec.file = &fakeRecordingWriteCloser{results: []recordingWriteResult{{err: writeErr}}}
	if err := o.Chunk(context.Background(), []byte("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := tab.StopRecording(); !errors.Is(err, writeErr) {
		t.Fatal(err)
	}
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	if _, err := tab.StopRecording(); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		state      RecordingState
		generation uint64
	}{
		{RecordingRunning, 1},
		{RecordingFailed, 1},
		{RecordingRunning, 2},
		{RecordingStopped, 2},
	}
	if len(events) != len(want) {
		t.Fatalf("events = %+v", events)
	}
	for i, expected := range want {
		if events[i].State != expected.state || events[i].Generation != expected.generation {
			t.Fatalf("event %d = %+v, want %+v", i, events[i], expected)
		}
	}
}

func TestRecordingConcurrentWriteStopStart(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	path := filepath.Join(t.TempDir(), "concurrent.log")
	var eventsMu sync.Mutex
	var events []RecordingStatus
	tab.SetRecordingHandler(func(status RecordingStatus) {
		eventsMu.Lock()
		events = append(events, status)
		eventsMu.Unlock()
	})
	const iterations = 20
	for i := 0; i < iterations; i++ {
		if err := tab.StartRecording(path); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 3)
		wg.Add(3)
		go func() {
			defer wg.Done()
			errs <- o.Chunk(context.Background(), []byte("chunk"))
		}()
		go func() {
			defer wg.Done()
			_, err := tab.StopRecording()
			errs <- err
		}()
		go func() {
			defer wg.Done()
			errs <- tab.StartRecording(path)
		}()
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := tab.StopRecording(); err != nil {
		t.Fatal(err)
	}
	status := tab.RecordingStatus()
	if status.State != RecordingStopped || status.Generation != 2*iterations {
		t.Fatalf("final status = %+v", status)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	for _, event := range events {
		if event.Generation > status.Generation {
			t.Fatalf("event from future generation: %+v, final %+v", event, status)
		}
	}
}

type recordingWriteResult struct {
	n   int
	err error
}

type fakeRecordingWriteCloser struct {
	mu         sync.Mutex
	results    []recordingWriteResult
	closeErr   error
	writeCalls int
	closeCalls int
}

func (f *fakeRecordingWriteCloser) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := f.writeCalls
	f.writeCalls++
	if index >= len(f.results) {
		return len(p), nil
	}
	result := f.results[index]
	return result.n, result.err
}

func (f *fakeRecordingWriteCloser) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	return f.closeErr
}

func (f *fakeRecordingWriteCloser) counts() (writes, closes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writeCalls, f.closeCalls
}

func TestDumpAndExportLog(t *testing.T) {
	tab := newTestTab(t)
	raw := []byte("log line\x1b[31m\xff\xfe\r\n")
	tab.Feed(raw)
	if got := tab.Dump(0); !bytes.Equal(got, raw) {
		t.Fatalf("dump = %q", got)
	}
	if got := tab.Dump(4); len(got) != 4 {
		t.Fatalf("dump(4) = %q", got)
	}
	path := filepath.Join(t.TempDir(), "export.log")
	n, err := tab.ExportLog(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != uint64(len(raw)) {
		t.Fatalf("exported = %d", n)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, raw) {
		t.Fatalf("export mismatch: %q", data)
	}
	if tab.LatestSeq() != uint64(len(raw)) || tab.BaseSeq() != 0 || tab.DroppedBytes() != 0 {
		t.Fatalf("seq accounting: base=%d latest=%d dropped=%d", tab.BaseSeq(), tab.LatestSeq(), tab.DroppedBytes())
	}
}

func TestDSRAndQueryResponses(t *testing.T) {
	responses := make(chan []byte, 16)
	tab := newTestTab(t, WithResponseHandler(func(p []byte) { responses <- p }))
	expect := func(what string) []byte {
		t.Helper()
		select {
		case r := <-responses:
			return r
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for %s", what)
			return nil
		}
	}
	tab.Feed([]byte("abc"))
	tab.Feed([]byte("\x1b[6n"))
	if got := expect("DSR"); !bytes.Equal(got, []byte("\x1b[1;4R")) {
		t.Fatalf("DSR = %q", got)
	}
	tab.Feed([]byte("\x1b["))
	tab.Feed([]byte("6n"))
	if got := expect("split DSR"); !bytes.Equal(got, []byte("\x1b[1;4R")) {
		t.Fatalf("split DSR = %q", got)
	}
	tab.Feed([]byte("\x1b[c"))
	got := expect("DA")
	if !bytes.HasPrefix(got, []byte("\x1b[?")) || !bytes.HasSuffix(got, []byte("c")) {
		t.Fatalf("DA = %q", got)
	}
}

func TestCloseIdempotentAndFeedAfterClose(t *testing.T) {
	tab := NewTab("t", "s", 80, 24, UTF8)
	tab.Feed([]byte("before"))
	tab.Close()
	tab.Close()
	tab.Feed([]byte("after"))
	if got := tab.Dump(0); !bytes.Equal(got, []byte("before")) {
		t.Fatalf("dump = %q", got)
	}
	if err := tab.Resize(100, 30); !errors.Is(err, ErrClosed) {
		t.Fatalf("resize after close = %v", err)
	}
}

func TestScreenHistory(t *testing.T) {
	tab := NewTab("t", "s", 80, 5, UTF8, WithScreenHistory(10))
	t.Cleanup(tab.Close)
	for i := 0; i < 50; i++ {
		tab.Feed([]byte(fmt.Sprintf("line-%d\r\n", i)))
	}
	if got := tab.ScrollbackLen(); got != 10 {
		t.Fatalf("scrollback len = %d", got)
	}
	hist := tab.ScreenHistory(3)
	if len(hist) != 3 {
		t.Fatalf("history = %q", hist)
	}
	want := []string{"line-43", "line-44", "line-45"}
	for i := range want {
		if hist[i] != want[i] {
			t.Fatalf("history = %q, want %q", hist, want)
		}
	}
	if all := tab.ScreenHistory(100); len(all) != 10 {
		t.Fatalf("history clamp = %d", len(all))
	}
}

func TestSnapshotLinesSemantics(t *testing.T) {
	tab := newTestTab(t)
	tab.Feed([]byte("ab  \r\n"))
	snap := tab.Snapshot()
	if len(snap.Lines) == 0 || snap.Lines[0] != "ab" {
		t.Fatalf("lines should trim trailing spaces: %q", snap.Lines)
	}
	for i, l := range snap.Lines {
		if i > 0 && l != "" {
			t.Fatalf("unexpected content at %d: %q", i, l)
		}
	}
}

func TestHighVolumeFeedMemoryBounded(t *testing.T) {
	if testing.Short() || raceDetectorEnabled {
		t.Skip("high-volume test skipped in -short and -race")
	}
	tab := newTestTab(t)
	chunk := make([]byte, 65536)
	for i := range chunk {
		chunk[i] = byte(i%251) + 1
	}
	const total = 40 * 1024 * 1024
	start := time.Now()
	for fed := 0; fed < total; fed += len(chunk) {
		tab.Feed(chunk)
	}
	t.Logf("fed %d MiB in %v (%.1f MiB/s)", total/1024/1024, time.Since(start), float64(total)/time.Since(start).Seconds()/1024/1024)
	if got := tab.Dump(1024); len(got) > 1024 {
		t.Fatalf("dump len = %d", len(got))
	}
	if tab.LatestSeq() != total {
		t.Fatalf("latest = %d", tab.LatestSeq())
	}
	if tab.DroppedBytes() != total-ScrollbackBytes {
		t.Fatalf("dropped = %d, want %d", tab.DroppedBytes(), total-ScrollbackBytes)
	}
	if got := len(tab.Dump(0)); got != ScrollbackBytes {
		t.Fatalf("retained = %d, want %d", got, ScrollbackBytes)
	}
	_ = tab.ScreenText()
}

func TestDefaultCapacities(t *testing.T) {
	if ScrollbackBytes != 32*1024*1024 || ScrollbackLines != 100_000 {
		t.Fatal("capacity constants changed")
	}
	tab := newTestTab(t)
	if tab.ring.Cap() != ScrollbackBytes {
		t.Fatalf("ring cap = %d", tab.ring.Cap())
	}
}
