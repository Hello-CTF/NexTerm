package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// fakeContextChannel adds the context-write capability to the fake channel;
// plain fakeChannel deliberately lacks it so fallback behavior stays pinned.
type fakeContextChannel struct {
	*fakeChannel
	writeContext func(context.Context, []byte) (int, error)
}

func (c *fakeContextChannel) WriteContext(ctx context.Context, p []byte) (int, error) {
	return c.writeContext(ctx, p)
}

// T-04: an in-progress write to a context-capable channel must abort when
// the caller's context ends, instead of parking inside the transport write.
func TestManagerWriteContextCancelsInFlightWrite(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	started := make(chan struct{})
	connector.transport(0).channelDecorator = func(channel *fakeChannel) base.Channel {
		return &fakeContextChannel{fakeChannel: channel, writeContext: func(ctx context.Context, p []byte) (int, error) {
			close(started)
			<-ctx.Done()
			return 0, context.Cause(ctx)
		}}
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- manager.Write(ctx, tab.ID, tab.Info().SizeOwner, []byte("stalled"))
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Write never reached the channel")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Write = %v, want context deadline exceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-progress write did not honor its context")
	}
}

// Channels without the context-write capability cannot abort a blocked
// Write, but the caller stops waiting for it when its context ends; the
// abandoned write still delivers its bytes once the transport recovers.
func TestManagerWriteWithoutContextCapabilityStopsWaitingOnCancel(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	started := make(chan struct{})
	release := make(chan struct{})
	stallFirstWrite(channel, started, release)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Write(ctx, tab.ID, tab.Info().SizeOwner, []byte("data"))
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Write never reached the channel")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrWriteStalled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("Write = %v, want ErrWriteStalled wrapping context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Write kept waiting for a blocked transport write after its context ended")
	}
	if writeGateHeld(tab) {
		t.Fatal("tab write gate still held after the caller gave up")
	}
	close(release)
	if err := manager.Write(context.Background(), tab.ID, tab.Info().SizeOwner, []byte("next")); err != nil {
		t.Fatalf("Write after recovery = %v", err)
	}
	if got := string(channel.written()); got != "datanext" {
		t.Fatalf("written = %q, want %q", got, "datanext")
	}
}

// Channel write failures are converted to session sentinels: a
// disconnected or closed channel surfaces as ErrDisconnected (our own pump
// closes the stream on reconnect, so a closed stream is recoverable) and a
// process the host reported as exited as ErrTabClosed.
func TestManagerWriteConvertsChannelErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"disconnected", base.ErrDisconnected, ErrDisconnected},
		{"stream closed", supervisor.ErrClosed, ErrDisconnected},
		{"process exited", supervisor.ErrExited, ErrTabClosed},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			manager, connector, _, session := openTestSession(t, KindSSH)
			connector.transport(0).channelDecorator = func(channel *fakeChannel) base.Channel {
				return &fakeContextChannel{fakeChannel: channel, writeContext: func(ctx context.Context, p []byte) (int, error) {
					return 0, testCase.err
				}}
			}
			tab := openTestTab(t, manager, session, "client-a", "a-1")
			err := manager.Write(context.Background(), tab.ID, tab.Info().SizeOwner, []byte("x"))
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Write = %v, want %v", err, testCase.want)
			}
		})
	}
}

// The paste paths share the same context-aware write: an in-flight literal
// paste write aborts on caller cancellation instead of parking.
func TestWritePasteLiteralContextCancelsInFlightWrite(t *testing.T) {
	manager, connector, terminals, connected := openOpenTestSession(t, KindLocal)
	started := make(chan struct{})
	connector.transport(0).channelDecorator = func(channel *fakeChannel) base.Channel {
		return &fakeContextChannel{fakeChannel: channel, writeContext: func(ctx context.Context, p []byte) (int, error) {
			close(started)
			<-ctx.Done()
			return 0, context.Cause(ctx)
		}}
	}
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	terminals.terminal(info.ID).setBracketedPaste(true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- manager.WritePasteLiteralFor(ctx, info.ID, "echo hi", false)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("paste write never reached the channel")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("WritePasteLiteralFor = %v, want context deadline exceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-progress paste write did not honor its context")
	}
}
