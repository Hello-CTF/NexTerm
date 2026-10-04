package production

import (
	"context"
	"errors"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
)

type terminalBridge struct {
	sessions *session.Manager
	factory  ipc.StreamFactory
	enabled  bool

	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	channels map[string]*terminalBridgeChannel
	wg       sync.WaitGroup
}

type terminalBridgeChannel struct {
	receiver *hub.Receiver
	stream   ipc.BinaryStream
	once     sync.Once
}

func newTerminalBridge(sessions *session.Manager, factory ipc.StreamFactory) *terminalBridge {
	_, sharedHub := factory.(hub.StreamFactory)
	return &terminalBridge{
		sessions: sessions, factory: factory,
		enabled:  sessions != nil && factory != nil && !sharedHub,
		channels: make(map[string]*terminalBridgeChannel),
	}
}

func (b *terminalBridge) Start(ctx context.Context) error {
	if !b.enabled {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ctx, b.cancel = context.WithCancel(ctx)
	return nil
}

func (b *terminalBridge) Bridge(channelID string) error {
	if !b.enabled {
		return nil
	}
	if channelID == "" {
		return ipc.BadParam(errors.New("channel id is empty"))
	}
	b.mu.Lock()
	if b.ctx == nil {
		b.mu.Unlock()
		return ipc.ErrStreamsUnavailable
	}
	if _, exists := b.channels[channelID]; exists {
		b.mu.Unlock()
		return nil
	}
	ctx := b.ctx
	b.mu.Unlock()

	receiver, err := b.sessions.Bind(channelID)
	if err != nil {
		return err
	}
	stream, err := b.factory.OpenBinary(ctx, ipc.ChannelRef{ID: channelID})
	if err != nil {
		_ = receiver.Close()
		return err
	}
	channel := &terminalBridgeChannel{receiver: receiver, stream: stream}
	b.mu.Lock()
	if b.ctx == nil || b.ctx.Err() != nil {
		b.mu.Unlock()
		channel.close()
		return context.Canceled
	}
	if _, exists := b.channels[channelID]; exists {
		b.mu.Unlock()
		channel.close()
		return nil
	}
	b.channels[channelID] = channel
	b.wg.Add(1)
	b.mu.Unlock()
	go b.run(ctx, channelID, channel)
	return nil
}

func (b *terminalBridge) Unbridge(channelID string) {
	b.mu.Lock()
	channel := b.channels[channelID]
	delete(b.channels, channelID)
	b.mu.Unlock()
	if channel != nil {
		channel.close()
	}
}

func (b *terminalBridge) run(ctx context.Context, channelID string, channel *terminalBridgeChannel) {
	defer b.wg.Done()
	defer func() {
		channel.close()
		b.mu.Lock()
		if b.channels[channelID] == channel {
			delete(b.channels, channelID)
		}
		b.mu.Unlock()
	}()
	for {
		frame, err := channel.receiver.Next(ctx)
		if err != nil {
			return
		}
		if frame.Kind != hub.FrameBinary {
			_ = channel.receiver.Ack(frame.Sequence)
			continue
		}
		if err := channel.stream.SendBinary(ctx, frame.Data); err != nil {
			return
		}
		_ = channel.receiver.Ack(frame.Sequence)
	}
}

func (b *terminalBridge) Shutdown(ctx context.Context) error {
	b.mu.Lock()
	cancel := b.cancel
	b.cancel = nil
	b.ctx = nil
	channels := make([]*terminalBridgeChannel, 0, len(b.channels))
	for _, channel := range b.channels {
		channels = append(channels, channel)
	}
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, channel := range channels {
		channel.close()
	}
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *terminalBridgeChannel) close() {
	c.once.Do(func() {
		_ = c.receiver.Close()
		_ = c.stream.Close()
	})
}

var _ Component = (*terminalBridge)(nil)
