package terminal

import (
	"bytes"
	"context"
	"errors"
	"testing"
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
	waitFor(t, "first frame on both subscribers", func() bool {
		return bytes.Equal(a.Bytes(), frame) && bytes.Equal(b.Bytes(), frame)
	})
	f.Send(context.Background(), []byte("2"))
	f.Send(context.Background(), []byte("3"))
	want := append(append([]byte{}, frame...), []byte("23")...)
	waitFor(t, "ordered frames on both subscribers", func() bool {
		return bytes.Equal(a.Bytes(), want) && bytes.Equal(b.Bytes(), want)
	})
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
		t.Fatal("good sink should be accepted")
	}
	waitFor(t, "failed sink detached and good sink delivered", func() bool {
		return f.Count() == 1 && bytes.Equal(good.Bytes(), []byte("x"))
	})
	f.Detach("good")
	f.Attach("bad2", &memSink{err: errors.New("gone")})
	f.Send(context.Background(), []byte("y"))
	waitFor(t, "second failed sink detached", func() bool { return f.Count() == 0 })
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
	waitFor(t, "replacement delivery", func() bool { return bytes.Equal(fresh.Bytes(), []byte("new")) })
	if len(old.Frames()) != 0 {
		t.Fatalf("stale sink got frames: %q", old.Bytes())
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

func TestFanoutCancellationSkipsPublishWithoutDetaching(t *testing.T) {
	f := NewFanout()
	sink := &memSink{}
	f.Attach("ws", sink)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if f.Send(ctx, []byte("x")) {
		t.Fatal("canceled publish should not be accepted")
	}
	if f.Count() != 1 {
		t.Fatalf("canceled publish detached subscriber: %d", f.Count())
	}
	if !f.Send(context.Background(), []byte("z")) {
		t.Fatal("subscriber should remain active")
	}
	waitFor(t, "delivery after canceled publish", func() bool { return bytes.Equal(sink.Bytes(), []byte("z")) })
}
