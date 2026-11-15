package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stallFirstWrite parks the first channel Write until release is closed and
// records every write's bytes once it completes.
func stallFirstWrite(channel *fakeChannel, started chan<- struct{}, release <-chan struct{}) {
	first := true
	channel.writeHook = func(data []byte) (int, error) {
		channel.mu.Lock()
		stall := first
		first = false
		channel.mu.Unlock()
		if stall {
			close(started)
			<-release
		}
		channel.mu.Lock()
		channel.input = append(channel.input, data...)
		channel.mu.Unlock()
		return len(data), nil
	}
}

// A stalled transport write must not hang an internal (AI) write: it returns
// at the caller's deadline, releases the tab write gate, and later writers
// wait for the abandoned write instead of interleaving bytes into it.
func TestWriteInternalStalledChannelReturnsAndKeepsOrder(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	started := make(chan struct{})
	release := make(chan struct{})
	stallFirstWrite(channel, started, release)
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.WriteInternal(ctx, tab.ID, []byte("first")) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("WriteInternal never reached the channel")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrWriteStalled) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("WriteInternal = %v, want ErrWriteStalled wrapping the deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WriteInternal hung on a stalled channel write")
	}
	if writeGateHeld(tab) {
		t.Fatal("tab write gate still held after the stalled write was abandoned")
	}

	// A later writer queues behind the abandoned write and gives up at its
	// own deadline without sending anything.
	second, cancelSecond := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelSecond()
	if err := manager.WriteInternal(second, tab.ID, []byte("second")); !errors.Is(err, ErrWriteStalled) {
		t.Fatalf("second WriteInternal = %v, want ErrWriteStalled", err)
	}
	if got := string(channel.written()); got != "" {
		t.Fatalf("written while the first write was stalled = %q, want nothing", got)
	}
	if writeGateHeld(tab) {
		t.Fatal("tab write gate held after the queued writer gave up")
	}

	released = true
	close(release)
	if err := manager.WriteInternal(context.Background(), tab.ID, []byte("third")); err != nil {
		t.Fatalf("WriteInternal after recovery = %v", err)
	}
	if got := string(channel.written()); got != "firstthird" {
		t.Fatalf("written = %q, want %q", got, "firstthird")
	}
}

// A writer whose context never ends still gives up after stalledWriteWait
// when an abandoned write keeps the channel blocked, so it never holds the
// tab write gate indefinitely.
func TestWriteBehindStalledWriteIsBounded(t *testing.T) {
	previous := stalledWriteWait
	stalledWriteWait = 20 * time.Millisecond
	t.Cleanup(func() { stalledWriteWait = previous })
	manager, connector, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	started := make(chan struct{})
	release := make(chan struct{})
	stallFirstWrite(channel, started, release)
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.WriteInternal(ctx, tab.ID, []byte("first")) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("WriteInternal never reached the channel")
	}
	cancel()
	if err := <-done; !errors.Is(err, ErrWriteStalled) {
		t.Fatalf("WriteInternal = %v, want ErrWriteStalled", err)
	}
	result := make(chan error, 1)
	go func() {
		result <- manager.Write(context.Background(), tab.ID, tab.Info().SizeOwner, []byte("typed"))
	}()
	select {
	case err := <-result:
		if !errors.Is(err, ErrWriteStalled) || errors.Is(err, context.Canceled) {
			t.Fatalf("Write = %v, want a bare ErrWriteStalled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Write parked behind a stalled write without a bound")
	}
	if writeGateHeld(tab) {
		t.Fatal("tab write gate held after the bounded wait failed")
	}
	if got := string(channel.written()); got != "" {
		t.Fatalf("written = %q, want nothing while the channel is stalled", got)
	}
}
