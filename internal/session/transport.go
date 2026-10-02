package session

import (
	"sync"
	"sync/atomic"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type transportHandle struct {
	base.Transport
	closed atomic.Bool
	once   sync.Once
	err    error
}

func newTransportHandle(transport base.Transport) *transportHandle {
	return &transportHandle{Transport: transport}
}

func (t *transportHandle) Close() error {
	t.once.Do(func() {
		t.closed.Store(true)
		t.err = t.Transport.Close()
	})
	return t.err
}

func (t *transportHandle) IsAlive() bool {
	return !t.closed.Load() && t.Transport.IsAlive()
}

type channelHandle struct {
	base.Channel
	once sync.Once
	err  error
}

func newChannelHandle(channel base.Channel) *channelHandle {
	return &channelHandle{Channel: channel}
}

func (c *channelHandle) Close() error {
	c.once.Do(func() { c.err = c.Channel.Close() })
	return c.err
}
