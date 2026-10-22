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
		done <- manager.Write(ctx, tab.ID, tab.Info().Controller, []byte("stalled"))
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

// Channels without the context-write capability keep the plain blocking
// behavior: an in-flight write is not aborted by context cancellation.
func TestManagerWriteWithoutContextCapabilityKeepsBlockingBehavior(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	started := make(chan struct{})
	release := make(chan struct{})
	channel.writeHook = func(data []byte) (int, error) {
		close(started)
		<-release
		channel.mu.Lock()
		channel.input = append(channel.input, data...)
		channel.mu.Unlock()
		return len(data), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Write(ctx, tab.ID, tab.Info().Controller, []byte("data"))
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Write never reached the channel")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("Write returned %v while the transport was still blocked; capability-less channels must keep the blocking behavior", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Write = %v", err)
	}
	if got := string(channel.written()); got != "data" {
		t.Fatalf("written = %q, want %q", got, "data")
	}
}

// Channel write failures are converted to session sentinels: a
// disconnected channel surfaces as ErrDisconnected and a remotely closed
// durable stream as ErrTabClosed, while caller cancellation passes through
// with its cause.
func TestManagerWriteConvertsChannelErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"disconnected", base.ErrDisconnected, ErrDisconnected},
		{"stream closed", supervisor.ErrClosed, ErrTabClosed},
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
			err := manager.Write(context.Background(), tab.ID, tab.Info().Controller, []byte("x"))
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
