package session

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"
)

// busyCoalescer returns a coalescer that flushed just now, so small chunks buffer behind the full window instead of leaving immediately.
func busyCoalescer(onTimer func()) *outCoalescer {
	c := newOutCoalescer(onTimer)
	c.lastFlush = time.Now()
	return c
}

func TestCoalesceFirstLargeChunkSentDirectly(t *testing.T) {
	c := newOutCoalescer(func() { t.Error("timer must not arm for a direct chunk") })
	big := bytes.Repeat([]byte("a"), outputCoalesceMax)
	batch, sendNow := c.add(big)
	if !sendNow || len(batch) != outputCoalesceMax {
		t.Fatalf("sendNow = %v batch = %d", sendNow, len(batch))
	}
	if got := c.take(); got != nil {
		t.Fatalf("direct chunk left %d buffered bytes", len(got))
	}
}

func TestCoalesceWindowAppends(t *testing.T) {
	fired := make(chan struct{}, 1)
	c := busyCoalescer(func() { fired <- struct{}{} })
	if _, sendNow := c.add([]byte("a")); sendNow {
		t.Fatal("small chunk must buffer")
	}
	if _, sendNow := c.add([]byte("b")); sendNow {
		t.Fatal("chunk below cap must buffer")
	}
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("coalesce window did not fire")
	}
	if got := c.take(); string(got) != "ab" {
		t.Fatalf("window batch = %q", got)
	}
	if got := c.take(); got != nil {
		t.Fatalf("flushed batch left %d bytes", len(got))
	}
}

func TestCoalesceCapDoesNotDrop(t *testing.T) {
	c := busyCoalescer(func() { t.Error("window must not fire before the cap") })
	first := bytes.Repeat([]byte("a"), outputCoalesceMax-1)
	if _, sendNow := c.add(first); sendNow {
		t.Fatal("chunk below cap must buffer")
	}
	batch, sendNow := c.add([]byte("bc"))
	if !sendNow || len(batch) != outputCoalesceMax+1 || !bytes.HasSuffix(batch, []byte("bc")) {
		t.Fatalf("sendNow = %v len = %d suffix = %q", sendNow, len(batch), batch[len(batch)-2:])
	}
	if got := c.take(); got != nil {
		t.Fatalf("cap flush left %d bytes", len(got))
	}
}

func TestCoalesceTeardownDiscardsResidual(t *testing.T) {
	c := busyCoalescer(func() { t.Error("teardown must stop the window") })
	if _, sendNow := c.add([]byte("tail")); sendNow {
		t.Fatal("small chunk must buffer")
	}
	c.teardown()
	if got := c.take(); got != nil {
		t.Fatalf("teardown retained %q for a later direct write", got)
	}
	if batch, sendNow := c.add([]byte("late")); sendNow || batch != nil {
		t.Fatalf("torn down coalescer accepted %q", batch)
	}
	c.teardown()
}

func TestFeedCoalescedFramesKeepOrder(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")

	const chunks = 20
	var want []byte
	for i := 0; i < chunks; i++ {
		chunk := []byte(fmt.Sprintf("chunk-%02d|", i))
		want = append(want, chunk...)
		if err := channel.emit(chunk); err != nil {
			t.Fatal(err)
		}
	}
	var got []byte
	frames := 0
	deadline := time.Now().Add(3 * time.Second)
	for len(got) < len(want) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		frame, err := receiver.Next(ctx)
		cancel()
		if err != nil {
			t.Fatalf("got %d of %d bytes in %d frames: %v", len(got), len(want), frames, err)
		}
		got = append(got, frame.Data...)
		frames++
		if err := receiver.Ack(frame.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("stream order broken: got %q want %q", got, want)
	}
	if frames >= chunks {
		t.Fatalf("no coalescing happened: %d frames for %d chunks", frames, chunks)
	}
}

func TestCoalesceWindowSelection(t *testing.T) {
	c := newOutCoalescer(func() {})
	c.followUp = true
	if got := c.windowLocked(true); got != outputCoalesceIdleGrace {
		t.Fatalf("after an immediate idle send: window = %v", got)
	}
	if got := c.windowLocked(false); got != outputCoalesceInterval {
		t.Fatalf("non-empty buffer: window = %v", got)
	}
	c.followUp = false
	if got := c.windowLocked(true); got != outputCoalesceInterval {
		t.Fatalf("sustained stream: window = %v", got)
	}
}

func TestCoalesceIdleChunkSentImmediately(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newOutCoalescer(func() { t.Error("timer must not arm for an idle chunk") })
	c.now = func() time.Time { return now }
	if batch, sendNow := c.add([]byte("x")); !sendNow || string(batch) != "x" {
		t.Fatalf("first chunk: sendNow = %v batch = %q", sendNow, batch)
	}
	c.take()
	now = now.Add(outputCoalesceInterval)
	if batch, sendNow := c.add([]byte("y")); !sendNow || string(batch) != "y" {
		t.Fatalf("chunk after an idle window: sendNow = %v batch = %q", sendNow, batch)
	}
}

func TestCoalesceFollowUpChunksShareOneGraceFrame(t *testing.T) {
	fired := make(chan struct{}, 4)
	c := newOutCoalescer(func() { fired <- struct{}{} })
	if _, sendNow := c.add([]byte("x")); !sendNow {
		t.Fatal("idle chunk must leave immediately")
	}
	// The chunks that follow arm the grace once; later chunks must neither re-arm nor flush, so one take returns them all.
	for i := 0; i < 3; i++ {
		if _, sendNow := c.add(bytes.Repeat([]byte{'a' + byte(i)}, 200)); sendNow {
			t.Fatal("follow-up chunk must buffer")
		}
	}
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("grace window did not fire")
	}
	if got := c.take(); len(got) != 600 {
		t.Fatalf("expected a single 600 byte frame, got %d", len(got))
	}
	select {
	case <-fired:
		t.Fatal("unexpected second flush")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestCoalesceSustainedStreamKeepsFullWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newOutCoalescer(func() {})
	c.now = func() time.Time { return now }
	c.add([]byte("a")) // idle start: sent immediately
	c.add([]byte("b")) // follow-up: armed with the grace
	c.take()
	now = now.Add(time.Millisecond)
	c.add([]byte("c"))
	c.mu.Lock()
	armed := c.timer != nil
	c.mu.Unlock()
	if !armed {
		t.Fatal("timer must be armed")
	}
	if got := c.windowLocked(true); got != outputCoalesceInterval {
		t.Fatalf("stream within one window of the last flush must keep the full window, got %v", got)
	}
	c.teardown()
}
