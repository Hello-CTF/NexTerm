package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

var (
	ErrClosed         = errors.New("hub channel closed")
	ErrDetached       = errors.New("hub channel detached")
	ErrReplaced       = errors.New("hub channel receiver replaced")
	ErrHubClosed      = errors.New("hub closed")
	ErrInvalidChannel = errors.New("hub channel id is empty")
	ErrInvalidJSON    = errors.New("hub JSON frame is invalid")
)

type FrameKind uint8

const (
	FrameBinary FrameKind = iota + 1
	FrameJSON
)

type Frame struct {
	Sequence uint64
	Kind     FrameKind
	Data     []byte
}

type Options struct {
	QueueFrames    int
	QueueBytes     int
	OnChannelClose func(channelID string)
	OnBackpressure func(channelID string, queuedBytes int)
}

type Hub struct {
	mu       sync.Mutex
	channels map[string]*channel
	closed   bool
	options  Options
}

type Stats struct {
	LiveChannels    int
	PendingChannels int
	QueuedFrames    int
	QueuedBytes     int
}

func New(options Options) *Hub {
	if options.QueueFrames <= 0 {
		options.QueueFrames = 512
	}
	if options.QueueBytes <= 0 {
		options.QueueBytes = 16 << 20
	}
	return &Hub{channels: make(map[string]*channel), options: options}
}

func (h *Hub) SendBinary(ctx context.Context, channelID string, data []byte) error {
	return h.send(ctx, channelID, Frame{Kind: FrameBinary, Data: data})
}

func (h *Hub) SendJSON(ctx context.Context, channelID string, data json.RawMessage) error {
	if !json.Valid(data) {
		return ErrInvalidJSON
	}
	return h.send(ctx, channelID, Frame{Kind: FrameJSON, Data: data})
}

func (h *Hub) send(ctx context.Context, channelID string, frame Frame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ch, err := h.channel(channelID)
	if err != nil {
		return err
	}
	frame.Data = append([]byte(nil), frame.Data...)
	return ch.send(ctx, frame)
}

func (h *Hub) Bind(channelID string) (*Receiver, error) {
	ch, err := h.channel(channelID)
	if err != nil {
		return nil, err
	}
	generation := ch.bind()
	if generation == 0 {
		return nil, ErrClosed
	}
	return &Receiver{hub: h, channelID: channelID, channel: ch, generation: generation}, nil
}

type Producer struct {
	hub          *Hub
	channelID    string
	channel      *channel
	generation   uint64
	forceOnce    sync.Once
	gracefulOnce sync.Once
}

func (h *Hub) Producer(channelID string) (*Producer, error) {
	ch, err := h.channel(channelID)
	if err != nil {
		return nil, err
	}
	ch.mu.Lock()
	if ch.closed || ch.draining {
		ch.mu.Unlock()
		return nil, ErrClosed
	}
	ch.producer++
	generation := ch.producer
	ch.signalLocked()
	ch.mu.Unlock()
	return &Producer{hub: h, channelID: channelID, channel: ch, generation: generation}, nil
}

func (p *Producer) SendBinary(ctx context.Context, data []byte) error {
	return p.send(ctx, Frame{Kind: FrameBinary, Data: data})
}

func (p *Producer) SendJSON(ctx context.Context, data json.RawMessage) error {
	if !json.Valid(data) {
		return ErrInvalidJSON
	}
	return p.send(ctx, Frame{Kind: FrameJSON, Data: data})
}

func (p *Producer) send(ctx context.Context, frame Frame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	frame.Data = append([]byte(nil), frame.Data...)
	return p.channel.sendProducer(ctx, frame, p.generation)
}

func (p *Producer) CloseGracefully() error {
	p.gracefulOnce.Do(func() {
		p.hub.mu.Lock()
		p.channel.mu.Lock()
		current := p.hub.channels[p.channelID] == p.channel && p.channel.producer == p.generation && !p.channel.closed
		if current {
			p.channel.draining = true
			if p.channel.pendingLocked() == 0 {
				delete(p.hub.channels, p.channelID)
				p.channel.closeLocked()
			} else {
				p.channel.signalLocked()
			}
		}
		p.channel.mu.Unlock()
		p.hub.mu.Unlock()
	})
	return nil
}

func (p *Producer) Close() error {
	p.forceOnce.Do(func() {
		p.hub.mu.Lock()
		p.channel.mu.Lock()
		current := p.hub.channels[p.channelID] == p.channel && p.channel.producer == p.generation && !p.channel.closed
		if current {
			delete(p.hub.channels, p.channelID)
			p.channel.closeLocked()
		}
		p.channel.mu.Unlock()
		p.hub.mu.Unlock()
	})
	return nil
}

func (h *Hub) DiscardPending(channelID string) error {
	if channelID == "" {
		return ErrInvalidChannel
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrHubClosed
	}
	if ch := h.channels[channelID]; ch != nil {
		if ch.discardPending() {
			delete(h.channels, channelID)
		}
	}
	return nil
}

func (h *Hub) CloseChannel(channelID string) error {
	if channelID == "" {
		return ErrInvalidChannel
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ErrHubClosed
	}
	ch := h.channels[channelID]
	delete(h.channels, channelID)
	h.mu.Unlock()
	if ch != nil {
		ch.close()
	}
	return nil
}

func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	channels := h.channels
	h.channels = make(map[string]*channel)
	h.mu.Unlock()
	for _, ch := range channels {
		ch.close()
	}
	return nil
}

func (h *Hub) Stats() Stats {
	h.mu.Lock()
	channels := make([]*channel, 0, len(h.channels))
	for _, ch := range h.channels {
		channels = append(channels, ch)
	}
	h.mu.Unlock()
	var stats Stats
	for _, ch := range channels {
		isBound, frames, bytes := ch.stats()
		if isBound {
			stats.LiveChannels++
		} else if frames > 0 {
			stats.PendingChannels++
		}
		stats.QueuedFrames += frames
		stats.QueuedBytes += bytes
	}
	return stats
}

func (h *Hub) channel(channelID string) (*channel, error) {
	if channelID == "" {
		return nil, ErrInvalidChannel
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrHubClosed
	}
	if ch := h.channels[channelID]; ch != nil {
		return ch, nil
	}
	ch := newChannel(channelID, h.options.QueueFrames, h.options.QueueBytes, h.options.OnBackpressure)
	h.channels[channelID] = ch
	return ch, nil
}

func (h *Hub) detach(channelID string, ch *channel, generation uint64) bool {
	ch.mu.Lock()
	detached := ch.bound && ch.generation == generation
	if detached {
		ch.bound = false
		ch.generation++
		ch.signalLocked()
	}
	ch.mu.Unlock()
	return detached
}

type Receiver struct {
	hub        *Hub
	channelID  string
	channel    *channel
	generation uint64
	closed     atomic.Bool
}

func (r *Receiver) Next(ctx context.Context) (Frame, error) {
	if r.closed.Load() {
		return Frame{}, ErrClosed
	}
	frame, drained, err := r.channel.next(ctx, r.generation)
	if drained {
		r.hub.finalizeDrained(r.channelID, r.channel)
	}
	return frame, err
}

func (r *Receiver) Close() error {
	if !r.closed.CompareAndSwap(false, true) {
		return nil
	}
	if r.hub.detach(r.channelID, r.channel, r.generation) && r.hub.options.OnChannelClose != nil {
		r.hub.options.OnChannelClose(r.channelID)
	}
	return nil
}

func (h *Hub) finalizeDrained(channelID string, ch *channel) {
	h.mu.Lock()
	if h.channels[channelID] == ch {
		delete(h.channels, channelID)
	}
	h.mu.Unlock()
}

type channel struct {
	mu             sync.Mutex
	changed        chan struct{}
	id             string
	queue          []Frame
	head           int
	queuedBytes    int
	nextSeq        uint64
	generation     uint64
	producer       uint64
	bound          bool
	draining       bool
	closed         bool
	backpressured  bool
	maxFrames      int
	maxBytes       int
	onBackpressure func(string, int)
}

func newChannel(id string, maxFrames, maxBytes int, onBackpressure func(string, int)) *channel {
	return &channel{changed: make(chan struct{}), id: id, maxFrames: maxFrames, maxBytes: maxBytes, onBackpressure: onBackpressure}
}

func (c *channel) send(ctx context.Context, frame Frame) error {
	return c.sendProducer(ctx, frame, 0)
}

func (c *channel) sendProducer(ctx context.Context, frame Frame, producer uint64) error {
	for {
		c.mu.Lock()
		if c.closed || c.draining {
			c.mu.Unlock()
			return ErrClosed
		}
		if producer != 0 && c.producer != producer {
			c.mu.Unlock()
			return ErrReplaced
		}
		if !c.fullLocked(len(frame.Data)) {
			c.nextSeq++
			frame.Sequence = c.nextSeq
			c.queue = append(c.queue, frame)
			c.queuedBytes += len(frame.Data)
			c.signalLocked()
			c.mu.Unlock()
			return nil
		}
		var callback func(string, int)
		queuedBytes := c.queuedBytes
		if !c.backpressured {
			c.backpressured = true
			callback = c.onBackpressure
		}
		changed := c.changed
		c.mu.Unlock()
		if callback != nil {
			callback(c.id, queuedBytes)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (c *channel) next(ctx context.Context, generation uint64) (Frame, bool, error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return Frame{}, false, ErrClosed
		}
		if !c.bound || c.generation != generation {
			replaced := c.generation > generation && c.bound
			c.mu.Unlock()
			if replaced {
				return Frame{}, false, ErrReplaced
			}
			return Frame{}, false, ErrDetached
		}
		if c.head < len(c.queue) {
			frame := c.queue[c.head]
			c.queue[c.head] = Frame{}
			c.head++
			c.queuedBytes -= len(frame.Data)
			if c.head == len(c.queue) {
				c.queue = nil
				c.head = 0
			} else if c.head >= 64 && c.head*2 >= len(c.queue) {
				copy(c.queue, c.queue[c.head:])
				c.queue = c.queue[:len(c.queue)-c.head]
				c.head = 0
			}
			if c.backpressured && c.pendingLocked() <= c.maxFrames/4 && c.queuedBytes <= c.maxBytes/4 {
				c.backpressured = false
			}
			drained := c.draining && c.pendingLocked() == 0
			if drained {
				c.closeLocked()
			} else {
				c.signalLocked()
			}
			c.mu.Unlock()
			return frame, drained, nil
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return Frame{}, false, ctx.Err()
		case <-changed:
		}
	}
}

func (c *channel) bind() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0
	}
	c.generation++
	c.bound = true
	c.signalLocked()
	return c.generation
}

func (c *channel) close() {
	c.mu.Lock()
	c.closeLocked()
	c.mu.Unlock()
}

func (c *channel) closeLocked() {
	if !c.closed {
		c.closed = true
		c.bound = false
		c.generation++
		c.queue = nil
		c.head = 0
		c.queuedBytes = 0
		c.signalLocked()
	}
}

func (c *channel) discardPending() bool {
	c.mu.Lock()
	c.queue = nil
	c.head = 0
	c.queuedBytes = 0
	c.backpressured = false
	finalized := c.draining
	if c.draining {
		c.closeLocked()
	} else {
		c.signalLocked()
	}
	c.mu.Unlock()
	return finalized
}

func (c *channel) stats() (bound bool, frames, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bound, c.pendingLocked(), c.queuedBytes
}

func (c *channel) pendingLocked() int {
	return len(c.queue) - c.head
}

func (c *channel) fullLocked(incoming int) bool {
	count := c.pendingLocked()
	if count >= c.maxFrames {
		return true
	}
	return count > 0 && c.queuedBytes+incoming > c.maxBytes
}

func (c *channel) signalLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (f Frame) String() string {
	return fmt.Sprintf("frame%d(kind=%d,bytes=%d)", f.Sequence, f.Kind, len(f.Data))
}
