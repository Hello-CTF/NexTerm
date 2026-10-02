package terminal

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestFanoutTwoSubscribersSameOutput(t *testing.T) {
	f := NewFanout()
	a, b := &memSink{}, &memSink{}
	f.Attach("ws-a", a)
	f.Attach("ws-b", b)
	if f.Count() != 2 {
		t.Fatalf("count = %d", f.Count())
	}
	frame := []byte("job is still running\r\n")
	if !f.Send(context.Background(), frame) {
		t.Fatal("expected delivery")
	}
	if !bytes.Equal(a.Bytes(), frame) || !bytes.Equal(b.Bytes(), frame) {
		t.Fatalf("a=%q b=%q", a.Bytes(), b.Bytes())
	}
	// Ordered per client across frames.
	f.Send(context.Background(), []byte("2"))
	f.Send(context.Background(), []byte("3"))
	if !bytes.Equal(a.Bytes(), append(append([]byte{}, frame...), []byte("23")...)) {
		t.Fatalf("order broken: %q", a.Bytes())
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("clients diverged")
	}
}

func TestFanoutNoSubscribersIsNoop(t *testing.T) {
	f := NewFanout()
	if f.Send(context.Background(), []byte("x")) {
		t.Fatal("no sinks: should return false")
	}
}

func TestFanoutFailingSinkDetached(t *testing.T) {
	f := NewFanout()
	bad := &memSink{err: errors.New("ws gone")}
	good := &memSink{}
	f.Attach("bad", bad)
	f.Attach("good", good)
	if !f.Send(context.Background(), []byte("x")) {
		t.Fatal("good sink should receive")
	}
	if f.Count() != 1 {
		t.Fatalf("failed sink not detached: %d", f.Count())
	}
	if !bytes.Equal(good.Bytes(), []byte("x")) {
		t.Fatalf("good = %q", good.Bytes())
	}
	// All sinks failing -> false and empty set.
	f.Detach("good")
	f.Attach("bad2", &memSink{err: errors.New("gone")})
	if f.Send(context.Background(), []byte("y")) {
		t.Fatal("expected false")
	}
	if f.Count() != 0 {
		t.Fatalf("count = %d", f.Count())
	}
}

func TestFanoutReattachReplaces(t *testing.T) {
	f := NewFanout()
	old, fresh := &memSink{}, &memSink{}
	if f.Attach("ws", old) {
		t.Fatal("first attach is not a replacement")
	}
	if !f.Attach("ws", fresh) {
		t.Fatal("second attach should replace")
	}
	f.Send(context.Background(), []byte("new"))
	if len(old.Frames()) != 0 {
		t.Fatalf("stale sink got frames: %q", old.Bytes())
	}
	if !bytes.Equal(fresh.Bytes(), []byte("new")) {
		t.Fatalf("fresh = %q", fresh.Bytes())
	}
	if f.Count() != 1 {
		t.Fatalf("count = %d", f.Count())
	}
}

func TestFanoutDetachAll(t *testing.T) {
	f := NewFanout()
	f.Attach("a", &memSink{})
	f.Attach("b", &memSink{})
	if n := f.DetachAll(); n != 2 || f.Count() != 0 {
		t.Fatalf("detachAll = %d count = %d", n, f.Count())
	}
	if f.Detach("missing") {
		t.Fatal("detach of missing id should be false")
	}
}

// Interrupted clients are detached (replay on re-attach restores their
// stream); they must never silently miss a frame and then continue.
func TestFanoutCancellationDetachesInterrupted(t *testing.T) {
	f := NewFanout()
	blocking := SinkFunc(func(ctx context.Context, data []byte) error {
		<-ctx.Done()
		return ctx.Err()
	})
	f.Attach("block", blocking)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if f.Send(ctx, []byte("x")) {
		t.Fatal("expected no delivery")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("send did not honor cancellation")
	}
	if f.Count() != 0 {
		t.Fatalf("interrupted sink retained: count = %d", f.Count())
	}

	// With a healthy sink alongside the blocked one, every retained
	// client has the complete sequence; detached ones receive nothing
	// more until they re-attach.
	g := NewFanout()
	g.Attach("block", blocking)
	good := &memSink{}
	g.Attach("good", good)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	g.Send(ctx2, []byte("y"))
	g.Send(context.Background(), []byte("z"))
	switch g.Count() {
	case 0:
		if len(good.Bytes()) != 0 {
			t.Fatalf("detached client kept receiving: %q", good.Bytes())
		}
	case 1:
		if !bytes.Equal(good.Bytes(), []byte("yz")) {
			t.Fatalf("continuity broken: %q", good.Bytes())
		}
	default:
		t.Fatalf("interrupted sink retained: count = %d", g.Count())
	}
}

func TestInflightCounter(t *testing.T) {
	i := NewInflight()
	i.Add(100)
	i.SubSaturating(30)
	if i.Load() != 70 {
		t.Fatalf("load = %d", i.Load())
	}
	i.SubSaturating(1000)
	if i.Load() != 0 {
		t.Fatalf("saturating sub = %d", i.Load())
	}
}

func TestInflightWaitBelow(t *testing.T) {
	i := NewInflight()
	i.Add(100)
	go func() {
		time.Sleep(20 * time.Millisecond)
		i.SubSaturating(60)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := i.WaitBelow(ctx, 50); err != nil {
		t.Fatal(err)
	}
	if i.Load() != 40 {
		t.Fatalf("load = %d", i.Load())
	}
}

func TestInflightWaitBelowCancellation(t *testing.T) {
	i := NewInflight()
	i.Add(100)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := i.WaitBelow(ctx, 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}
