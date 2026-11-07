package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// fakeWindowedChannel models a supervisor stream: WriteContext waits for an
// acknowledgement that never arrives, WriteWindowed returns once the bytes are
// on the wire.
type fakeWindowedChannel struct {
	*fakeChannel
	mu       sync.Mutex
	windowed []byte
	err      error
	acked    chan struct{}
}

func (c *fakeWindowedChannel) WriteContext(ctx context.Context, p []byte) (int, error) {
	select {
	case <-c.acked:
		return len(p), nil
	case <-ctx.Done():
		return 0, context.Cause(ctx)
	}
}

func (c *fakeWindowedChannel) WriteWindowed(_ context.Context, p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0, c.err
	}
	c.windowed = append(c.windowed, p...)
	return len(p), nil
}

func (c *fakeWindowedChannel) sent() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.windowed)
}

func openWindowedTestTab(t *testing.T, err error) (*Manager, *Tab, *fakeWindowedChannel) {
	t.Helper()
	manager, connector, _, session := openTestSession(t, KindSSH)
	created := make(chan *fakeWindowedChannel, 1)
	connector.transport(0).channelDecorator = func(channel *fakeChannel) base.Channel {
		windowed := &fakeWindowedChannel{fakeChannel: channel, err: err, acked: make(chan struct{})}
		created <- windowed
		return windowed
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	select {
	case channel := <-created:
		return manager, tab, channel
	case <-time.After(5 * time.Second):
		t.Fatal("channel was not created")
		return nil, nil, nil
	}
}

// Keystrokes must not wait for the host's acknowledgement of earlier input:
// each Write returns as soon as its bytes are on the wire, in order.
func TestManagerWriteDoesNotWaitForInputAcknowledgement(t *testing.T) {
	manager, tab, channel := openWindowedTestTab(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, key := range []string{"l", "s", "\r"} {
		if err := manager.Write(ctx, tab.ID, tab.Info().SizeOwner, []byte(key)); err != nil {
			t.Fatalf("Write(%q) = %v; it waited for an acknowledgement", key, err)
		}
	}
	if got := channel.sent(); got != "ls\r" {
		t.Fatalf("sent %q, want %q", got, "ls\r")
	}
}

// A rejection the stream reports on a later write keeps the session error
// mapping of acknowledged writes.
func TestManagerWriteWindowedConvertsChannelErrors(t *testing.T) {
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
			manager, tab, _ := openWindowedTestTab(t, testCase.err)
			err := manager.Write(context.Background(), tab.ID, tab.Info().SizeOwner, []byte("x"))
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Write = %v, want %v", err, testCase.want)
			}
		})
	}
}
