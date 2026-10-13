package session

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
)

func TestCoalesceFirstLargeChunkSentDirectly(t *testing.T) {
	c := newOutCoalescer(func() { t.Error("timer must not arm for a direct chunk") })
	big := bytes.Repeat([]byte("a"), outputCoalesceMax)
	batch, sendNow := c.add(big)
	if !sendNow || len(batch) != outputCoalesceMax {
		t.Fatalf("sendNow = %v batch = %d", sendNow, len(batch))
	}
	c.sent()
	if got := c.take(); got != nil {
		t.Fatalf("direct chunk left %d buffered bytes", len(got))
	}
}

func TestCoalesceWindowAppends(t *testing.T) {
	fired := make(chan struct{}, 1)
	c := newOutCoalescer(func() { fired <- struct{}{} })
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
	c.sent()
	if got := c.take(); got != nil {
		t.Fatalf("flushed batch left %d bytes", len(got))
	}
}

func TestCoalesceCapDoesNotDrop(t *testing.T) {
	c := newOutCoalescer(func() { t.Error("window must not fire before the cap") })
	first := bytes.Repeat([]byte("a"), outputCoalesceMax-1)
	if _, sendNow := c.add(first); sendNow {
		t.Fatal("chunk below cap must buffer")
	}
	batch, sendNow := c.add([]byte("bc"))
	if !sendNow || len(batch) != outputCoalesceMax+1 || !bytes.HasSuffix(batch, []byte("bc")) {
		t.Fatalf("sendNow = %v len = %d suffix = %q", sendNow, len(batch), batch[len(batch)-2:])
	}
	c.sent()
	if got := c.take(); got != nil {
		t.Fatalf("cap flush left %d bytes", len(got))
	}
}

func TestCoalesceTeardownFlushesBestEffort(t *testing.T) {
	bus := hub.New(hub.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	producer, err := bus.Producer("teardown")
	if err != nil {
		t.Fatal(err)
	}
	c := newOutCoalescer(func() { t.Error("teardown must stop the window") })
	if _, sendNow := c.add([]byte("tail")); sendNow {
		t.Fatal("small chunk must buffer")
	}
	c.teardown(producer)
	receiver, err := bus.Bind("teardown")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := receiver.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(frame.Data) != "tail" {
		t.Fatalf("teardown flush = %q", frame.Data)
	}
	if _, sendNow := c.add([]byte("late")); sendNow {
		t.Fatal("torn down coalescer must drop")
	}
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
