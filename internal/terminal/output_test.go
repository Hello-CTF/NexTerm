package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestOutputVisibleImmediateDelivery(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	sink := &memSink{}
	o.Attach("a", sink)
	raw := []byte("hello \x1b[31mred\x1b[0m\r\n")
	if err := o.Chunk(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sink.Bytes(), raw) {
		t.Fatalf("sink = %q", sink.Bytes())
	}
	if !bytes.Equal(tab.Dump(0), raw) {
		t.Fatalf("ring = %q", tab.Dump(0))
	}
	if !containsLine(tab.ScreenText(), "hello red") {
		t.Fatalf("screen = %q", tab.ScreenText())
	}
}

func containsLine(text, want string) bool {
	return bytes.Contains([]byte(text), []byte(want))
}

func TestOutputHiddenBatching(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	sink := &memSink{}
	o.Attach("a", sink)
	o.SetVisible(false)
	ctx := context.Background()
	if err := o.Chunk(ctx, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := o.Chunk(ctx, []byte("b")); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.Frames()); n != 0 {
		t.Fatalf("hidden tab should batch, got %d frames", n)
	}
	waitFor(t, "hidden flush", func() bool { return len(sink.Frames()) == 1 })
	if !bytes.Equal(sink.Bytes(), []byte("ab")) {
		t.Fatalf("batch = %q", sink.Bytes())
	}
	if !bytes.Equal(tab.Dump(0), []byte("ab")) {
		t.Fatalf("ring = %q", tab.Dump(0))
	}
}

func TestOutputHiddenBatchMaxFlushesImmediately(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	sink := &memSink{}
	o.Attach("a", sink)
	o.SetVisible(false)
	ctx := context.Background()
	o.chunkMu.Lock()
	o.pending = bytes.Repeat([]byte{'x'}, HiddenBatchMax)
	o.chunkMu.Unlock()
	if err := o.Chunk(ctx, []byte("y")); err != nil {
		t.Fatal(err)
	}
	if len(sink.Frames()) != 1 || sink.Len() != HiddenBatchMax+1 {
		t.Fatalf("frames=%d bytes=%d", len(sink.Frames()), sink.Len())
	}
	got := sink.Bytes()
	if got[0] != 'x' || got[len(got)-1] != 'y' {
		t.Fatalf("batch order broken: %c ... %c", got[0], got[len(got)-1])
	}
}

func TestOutputVisibleAgainFlushesPromptly(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	sink := &memSink{}
	o.Attach("a", sink)
	o.SetVisible(false)
	if err := o.Chunk(context.Background(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	o.SetVisible(true)
	waitFor(t, "visibility flush", func() bool { return sink.Len() == 1 })
	if err := o.Chunk(context.Background(), []byte("y")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sink.Bytes(), []byte("xy")) {
		t.Fatalf("sink = %q", sink.Bytes())
	}
}

func TestOutputConsumeFlushesOnClose(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	sink := &memSink{}
	o.Attach("a", sink)
	o.SetVisible(false)
	chunks := make(chan []byte, 2)
	chunks <- []byte("one")
	chunks <- []byte("two")
	close(chunks)
	if err := o.Consume(context.Background(), chunks); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sink.Bytes(), []byte("onetwo")) {
		t.Fatalf("sink = %q", sink.Bytes())
	}
}

func TestOutputThrottlePauseResumeAndCancel(t *testing.T) {
	tab := newTestTab(t)
	throttled := make(chan int64, 4)
	o := newTestOutput(t, tab, WithThrottleHandler(func(n int64) { throttled <- n }))
	sink := &memSink{}
	o.Attach("a", sink)
	o.Inflight().Add(InflightPause + 1000)

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- o.Chunk(ctx, []byte("z")) }()
	select {
	case <-done:
		t.Fatal("chunk should block above InflightPause")
	case <-time.After(40 * time.Millisecond):
	}
	select {
	case n := <-throttled:
		if n < InflightPause {
			t.Fatalf("throttle callback n = %d", n)
		}
	case <-time.After(time.Second):
		t.Fatal("throttle callback not fired")
	}
	o.Inflight().SubSaturating(2000)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("chunk did not resume")
	}
	if !bytes.Equal(sink.Bytes(), []byte("z")) {
		t.Fatalf("sink = %q", sink.Bytes())
	}

	o.Inflight().Add(InflightPause + 1000)
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := o.Chunk(cctx, []byte("q")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestOutputAttachReplayNoGapNoDuplicate(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	ctx := context.Background()

	var fedMu sync.Mutex
	var fed bytes.Buffer
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for i := 0; i < 300; i++ {
			frame := []byte(fmt.Sprintf("%04d|", i))
			fedMu.Lock()
			fed.Write(frame)
			fedMu.Unlock()
			if err := o.Chunk(ctx, frame); err != nil {
				return
			}
			runtime.Gosched()
		}
	}()

	time.Sleep(5 * time.Millisecond)
	sink := &memSink{}
	const replay = 33
	if err := o.AttachReplay(ctx, "a", sink, replay); err != nil {
		t.Fatal(err)
	}
	seq := tab.LatestSeq()
	<-producerDone
	waitFor(t, "live frames drained", func() bool {
		return uint64(sink.Len()) >= tab.LatestSeq()-seq+minUint64(uint64(replay), seq)
	})

	frames := sink.Frames()
	if len(frames) == 0 {
		t.Fatal("no replay frame")
	}
	replayLen := uint64(len(frames[0]))
	if replayLen != minUint64(replay, seq) {
		t.Fatalf("replay len = %d, seq = %d", replayLen, seq)
	}
	fedMu.Lock()
	all := bytes.Clone(fed.Bytes())
	fedMu.Unlock()
	want := all[seq-replayLen:]
	if got := sink.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("client stream mismatch: got %d bytes want %d bytes", len(got), len(want))
	}
}

func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func TestOutputAttachFromSequence(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	ctx := context.Background()
	if err := o.Chunk(ctx, []byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	sink := &memSink{}
	next, attached, err := o.AttachFrom(ctx, "a", sink, 4, 0)
	if err != nil || next != 10 || !attached {
		t.Fatalf("next = %d attached = %v err = %v", next, attached, err)
	}
	if err := o.Chunk(ctx, []byte("X")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sink.Bytes(), []byte("456789X")) {
		t.Fatalf("sink = %q", sink.Bytes())
	}
	sinkC := &memSink{}
	next, attached, err = o.AttachFrom(ctx, "c", sinkC, 999, 0)
	if err != nil || next != 11 || !attached {
		t.Fatalf("beyond-total next = %d attached = %v err = %v", next, attached, err)
	}
	if len(sinkC.Frames()) != 0 {
		t.Fatalf("sinkC replay = %q", sinkC.Bytes())
	}
}

func TestOutputAttachFromBoundedReplayCatchUp(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	ctx := context.Background()
	if err := o.Chunk(ctx, []byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	sink := &memSink{}
	seq := uint64(0)
	for round := 0; ; round++ {
		if round > 10 {
			t.Fatal("catch-up did not converge")
		}
		next, attached, err := o.AttachFrom(ctx, "b", sink, seq, 3)
		if err != nil {
			t.Fatal(err)
		}
		if attached {
			if next != 10 {
				t.Fatalf("attached at %d, want head 10", next)
			}
			break
		}
		if o.SubscriberCount() != 0 {
			t.Fatal("truncated replay must not attach live")
		}
		if next <= seq {
			t.Fatalf("no progress: seq = %d next = %d", seq, next)
		}
		seq = next
	}
	if err := o.Chunk(ctx, []byte("X")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sink.Bytes(), []byte("0123456789X")) {
		t.Fatalf("sink = %q", sink.Bytes())
	}
}

func TestOutputAttachReplayFailureNotAttached(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	if err := o.Chunk(context.Background(), []byte("data")); err != nil {
		t.Fatal(err)
	}
	bad := &memSink{err: errors.New("closed")}
	if err := o.AttachReplay(context.Background(), "bad", bad, 0); err == nil {
		t.Fatal("expected replay error")
	}
	if o.SubscriberCount() != 0 {
		t.Fatal("failed sink must not be attached")
	}
}

func TestPumpReader(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	sink := &memSink{}
	o.Attach("a", sink)
	r, w := io.Pipe()
	go func() {
		_, _ = w.Write([]byte("hello "))
		_, _ = w.Write([]byte("pump"))
		_ = w.Close()
	}()
	if err := PumpReader(context.Background(), r, o); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sink.Bytes(), []byte("hello pump")) {
		t.Fatalf("sink = %q", sink.Bytes())
	}
	if o.Inflight().Load() != 0 {
		t.Fatalf("inflight = %d", o.Inflight().Load())
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestPumpReaderReadError(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	boom := errors.New("pty died")
	if err := PumpReader(context.Background(), errReader{err: boom}, o); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestPumpReaderCancellation(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	r, w := io.Pipe()
	defer w.Close()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := PumpReader(ctx, r, o); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}
