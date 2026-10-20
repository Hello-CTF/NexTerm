package terminal

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
)

var ErrFanoutQueueFull = errors.New("terminal: subscriber queue full")

const (
	defaultFanoutQueueFrames = 64
	defaultFanoutQueueBytes  = 4 << 20
)

type Sink interface {
	SendBinary(ctx context.Context, data []byte) error
}

type SinkFunc func(ctx context.Context, data []byte) error

func (f SinkFunc) SendBinary(ctx context.Context, data []byte) error { return f(ctx, data) }

// FanoutSubscriberStats reports queued plus in-flight lag, frames rejected or discarded by the bounded queue,
// active send failures, and abnormal disconnects that require replay. Intentional detach is not a gap.
type FanoutSubscriberStats struct {
	LagFrames     int
	LagBytes      int
	DroppedFrames uint64
	DroppedBytes  uint64
	Gaps          uint64
}

type FanoutSubscriberOptions struct {
	// OnDisconnect runs after an abnormal removal so the owner can close the stream and recover through replay.
	OnDisconnect func(stats FanoutSubscriberStats, cause error)
}

type FanoutOptions struct {
	QueueFrames int
	QueueBytes  int
	Logger      *slog.Logger
}

type fanoutEntry struct {
	id           string
	gen          uint64
	sink         Sink
	onDisconnect func(FanoutSubscriberStats, error)
	maxFrames    int
	maxBytes     int

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}

	mu            sync.Mutex
	queue         [][]byte
	head          int
	lagFrames     int
	lagBytes      int
	inFlight      bool
	stopped       bool
	droppedFrames uint64
	droppedBytes  uint64
	gaps          uint64

	stopOnce sync.Once
}

type Fanout struct {
	mu     sync.Mutex
	sendMu sync.Mutex
	next   uint64
	sinks  map[string]*fanoutEntry
	frames int
	bytes  int
	logger *slog.Logger
}

func NewFanout() *Fanout {
	return NewFanoutWithOptions(FanoutOptions{})
}

func NewFanoutWithOptions(options FanoutOptions) *Fanout {
	if options.QueueFrames <= 0 {
		options.QueueFrames = defaultFanoutQueueFrames
	}
	if options.QueueBytes <= 0 {
		options.QueueBytes = defaultFanoutQueueBytes
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Fanout{
		sinks:  make(map[string]*fanoutEntry),
		frames: options.QueueFrames,
		bytes:  options.QueueBytes,
		logger: options.Logger,
	}
}

func (f *Fanout) Attach(id string, sink Sink) bool {
	return f.AttachWithOptions(id, sink, FanoutSubscriberOptions{})
}

func (f *Fanout) AttachWithOptions(id string, sink Sink, options FanoutSubscriberOptions) bool {
	ctx, cancel := context.WithCancel(context.Background())
	entry := &fanoutEntry{
		id: id, sink: sink, onDisconnect: options.OnDisconnect, maxFrames: f.frames, maxBytes: f.bytes,
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	f.mu.Lock()
	f.next++
	entry.gen = f.next
	old := f.sinks[id]
	f.sinks[id] = entry
	f.mu.Unlock()
	if old != nil {
		old.stop()
	}
	go f.run(entry)
	return old != nil
}

func (f *Fanout) Detach(id string) bool {
	f.mu.Lock()
	entry := f.sinks[id]
	if entry != nil {
		delete(f.sinks, id)
	}
	f.mu.Unlock()
	if entry == nil {
		return false
	}
	entry.stop()
	return true
}

func (f *Fanout) DetachAll() int {
	f.mu.Lock()
	entries := make([]*fanoutEntry, 0, len(f.sinks))
	for id, entry := range f.sinks {
		entries = append(entries, entry)
		delete(f.sinks, id)
	}
	f.mu.Unlock()
	for _, entry := range entries {
		entry.stop()
	}
	return len(entries)
}

func (f *Fanout) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sinks)
}

func (f *Fanout) SubscriberStats(id string) (FanoutSubscriberStats, bool) {
	f.mu.Lock()
	entry := f.sinks[id]
	f.mu.Unlock()
	if entry == nil {
		return FanoutSubscriberStats{}, false
	}
	return entry.stats(), true
}

func (f *Fanout) detachIf(entry *fanoutEntry) {
	f.mu.Lock()
	if f.sinks[entry.id] == entry {
		delete(f.sinks, entry.id)
	}
	f.mu.Unlock()
}

func (f *Fanout) Send(ctx context.Context, data []byte) bool {
	if ctx.Err() != nil {
		return false
	}
	f.sendMu.Lock()
	defer f.sendMu.Unlock()

	f.mu.Lock()
	targets := make([]*fanoutEntry, 0, len(f.sinks))
	for _, entry := range f.sinks {
		targets = append(targets, entry)
	}
	f.mu.Unlock()
	accepted := false
	for _, entry := range targets {
		if f.sendTo(entry, data) {
			accepted = true
		}
	}
	return accepted
}

func (f *Fanout) SendTo(id string, data []byte) bool {
	f.sendMu.Lock()
	defer f.sendMu.Unlock()
	f.mu.Lock()
	entry := f.sinks[id]
	f.mu.Unlock()
	return entry != nil && f.sendTo(entry, data)
}

func (f *Fanout) sendTo(entry *fanoutEntry, data []byte) bool {
	accepted, full := entry.enqueue(data)
	if accepted {
		return true
	}
	if full {
		f.abort(entry, ErrFanoutQueueFull, data)
	}
	return false
}

func (f *Fanout) abort(entry *fanoutEntry, cause error, rejected []byte) {
	aborted := false
	var stats FanoutSubscriberStats
	entry.stopOnce.Do(func() {
		entry.mu.Lock()
		waiting := len(entry.queue) - entry.head
		waitingBytes := 0
		for _, queued := range entry.queue[entry.head:] {
			waitingBytes += len(queued)
		}
		entry.queue = nil
		entry.head = 0
		entry.lagFrames -= waiting
		entry.lagBytes -= waitingBytes
		entry.droppedFrames += uint64(waiting)
		entry.droppedBytes += uint64(waitingBytes)
		if rejected != nil {
			entry.droppedFrames++
			entry.droppedBytes += uint64(len(rejected))
		}
		entry.gaps++
		entry.stopped = true
		stats = entry.statsLocked()
		entry.mu.Unlock()
		close(entry.done)
		entry.cancel()
		aborted = true
	})
	if !aborted {
		return
	}
	f.detachIf(entry)
	if entry.onDisconnect != nil {
		entry.onDisconnect(stats, cause)
	} else {
		f.logger.Warn("terminal subscriber disconnected; replay required",
			"subscriber", entry.id,
			"lagFrames", stats.LagFrames,
			"lagBytes", stats.LagBytes,
			"droppedFrames", stats.DroppedFrames,
			"droppedBytes", stats.DroppedBytes,
			"gaps", stats.Gaps,
			"error", cause,
		)
	}
}

func (f *Fanout) run(entry *fanoutEntry) {
	for {
		delivery, ok := entry.next()
		if !ok {
			return
		}
		err := entry.sink.SendBinary(entry.ctx, delivery)
		if !entry.finish(delivery, err) {
			if err != nil {
				f.abort(entry, err, nil)
			}
			return
		}
	}
}

func (e *fanoutEntry) enqueue(data []byte) (accepted, full bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return false, false
	}
	if e.lagFrames >= e.maxFrames || e.lagFrames > 0 && e.lagBytes+len(data) > e.maxBytes {
		return false, true
	}
	e.queue = append(e.queue, bytes.Clone(data))
	e.lagFrames++
	e.lagBytes += len(data)
	select {
	case e.wake <- struct{}{}:
	default:
	}
	return true, false
}

func (e *fanoutEntry) next() ([]byte, bool) {
	for {
		e.mu.Lock()
		if e.stopped {
			e.mu.Unlock()
			return nil, false
		}
		if e.head < len(e.queue) {
			delivery := e.queue[e.head]
			e.queue[e.head] = nil
			e.head++
			e.inFlight = true
			if e.head == len(e.queue) {
				e.queue = nil
				e.head = 0
			}
			e.mu.Unlock()
			return delivery, true
		}
		e.mu.Unlock()
		select {
		case <-e.wake:
		case <-e.done:
			return nil, false
		}
	}
}

func (e *fanoutEntry) finish(delivery []byte, sendErr error) bool {
	e.mu.Lock()
	if e.inFlight {
		e.lagFrames--
		e.lagBytes -= len(delivery)
		e.inFlight = false
	}
	active := !e.stopped
	if sendErr != nil && active {
		e.droppedFrames++
		e.droppedBytes += uint64(len(delivery))
	}
	e.mu.Unlock()
	return sendErr == nil || !active
}

func (e *fanoutEntry) stop() {
	e.stopOnce.Do(func() {
		e.mu.Lock()
		waiting := len(e.queue) - e.head
		waitingBytes := 0
		for _, queued := range e.queue[e.head:] {
			waitingBytes += len(queued)
		}
		e.queue = nil
		e.head = 0
		e.lagFrames -= waiting
		e.lagBytes -= waitingBytes
		e.stopped = true
		e.mu.Unlock()
		close(e.done)
		e.cancel()
	})
}

func (e *fanoutEntry) stats() FanoutSubscriberStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statsLocked()
}

func (e *fanoutEntry) statsLocked() FanoutSubscriberStats {
	return FanoutSubscriberStats{
		LagFrames: e.lagFrames, LagBytes: e.lagBytes,
		DroppedFrames: e.droppedFrames, DroppedBytes: e.droppedBytes, Gaps: e.gaps,
	}
}
