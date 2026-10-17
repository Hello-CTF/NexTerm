package session

import (
	"sync"
	"sync/atomic"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type transportHandle struct {
	base.Transport
	closed   atomic.Bool
	mu       sync.Mutex
	complete bool
	err      error
}

func newTransportHandle(transport base.Transport) *transportHandle {
	return &transportHandle{Transport: transport}
}

func (t *transportHandle) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.complete {
		return t.err
	}
	t.closed.Store(true)
	t.err = t.Transport.Close()
	t.complete = t.err == nil
	return t.err
}

func (t *transportHandle) IsAlive() bool {
	return !t.closed.Load() && t.Transport.IsAlive()
}

type channelHandle struct {
	base.Channel
	mu       sync.Mutex
	complete bool
	err      error
}

func newChannelHandle(channel base.Channel) *channelHandle {
	return &channelHandle{Channel: channel}
}

func (c *channelHandle) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.complete {
		return c.err
	}
	c.err = c.Channel.Close()
	c.complete = c.err == nil
	return c.err
}
