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
	SendJSON(ctx context.Context, data []byte) error
}

type SinkFunc func(ctx context.Context, data []byte) error

func (f SinkFunc) SendBinary(ctx context.Context, data []byte) error { return f(ctx, data) }

// SinkFunc delivers JSON frames through the same function; sinks that must
// preserve the frame kind use SinkFuncs.
func (f SinkFunc) SendJSON(ctx context.Context, data []byte) error { return f(ctx, data) }

// SinkFuncs adapts two functions to a Sink, preserving the frame kind.
type SinkFuncs struct {
	Binary func(ctx context.Context, data []byte) error
	JSON   func(ctx context.Context, data []byte) error
}

func (s SinkFuncs) SendBinary(ctx context.Context, data []byte) error { return s.Binary(ctx, data) }
func (s SinkFuncs) SendJSON(ctx context.Context, data []byte) error   { return s.JSON(ctx, data) }

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

type fanoutFrame struct {
	json bool
	data []byte
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
	space  chan struct{}

	mu            sync.Mutex
	queue         []fanoutFrame // ring buffer, capacity bounded by maxFrames
	head          int           // index of the oldest retained frame
	queued        int           // frames retained in the ring
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
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), space: make(chan struct{}, 1),
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

// Has reports whether id is attached; an aborted subscriber is removed.
func (f *Fanout) Has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sinks[id] != nil
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
		if f.sendTo(entry, data, false) {
			accepted = true
		}
	}
	return accepted
}

func (f *Fanout) SendTo(id string, data []byte) bool {
	return f.sendFrameTo(id, fanoutFrame{data: data})
}

// SendJSONTo enqueues a JSON control frame for one subscriber, ordered with
// binary output already queued for it.
func (f *Fanout) SendJSONTo(id string, data []byte) bool {
	return f.sendFrameTo(id, fanoutFrame{json: true, data: data})
}

// SendToWait enqueues a frame for one subscriber, waiting for queue space
// instead of aborting the subscriber when the queue is full. It returns
// false only when the subscriber is gone or the context ends.
//
// It deliberately does not take the fanout send mutex while waiting: the
// recovery path holds the subscriber paused, so no live frames can reorder
// into the replay, and other subscribers' live output must not starve behind
// a slow reader.
func (f *Fanout) SendToWait(ctx context.Context, id string, data []byte) bool {
	return f.sendFrameToWait(ctx, id, fanoutFrame{data: data})
}

// SendJSONToWait enqueues a JSON control frame like SendToWait.
func (f *Fanout) SendJSONToWait(ctx context.Context, id string, data []byte) bool {
	return f.sendFrameToWait(ctx, id, fanoutFrame{json: true, data: data})
}

func (f *Fanout) sendFrameToWait(ctx context.Context, id string, frame fanoutFrame) bool {
	f.mu.Lock()
	entry := f.sinks[id]
	f.mu.Unlock()
	if entry == nil {
		return false
	}
	for {
		accepted, full := entry.enqueue(frame)
		if accepted {
			return true
		}
		if !full {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-entry.done:
			return false
		case <-entry.space:
		}
	}
}

func (f *Fanout) sendFrameTo(id string, frame fanoutFrame) bool {
	f.sendMu.Lock()
	defer f.sendMu.Unlock()
	f.mu.Lock()
	entry := f.sinks[id]
	f.mu.Unlock()
	return entry != nil && f.sendTo(entry, frame.data, frame.json)
}

func (f *Fanout) sendTo(entry *fanoutEntry, data []byte, jsonFrame bool) bool {
	accepted, full := entry.enqueue(fanoutFrame{json: jsonFrame, data: data})
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
		waiting, waitingBytes := entry.retainedLocked()
		entry.queue = nil
		entry.head = 0
		entry.queued = 0
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
		var err error
		if delivery.json {
			err = entry.sink.SendJSON(entry.ctx, delivery.data)
		} else {
			err = entry.sink.SendBinary(entry.ctx, delivery.data)
		}
		if !entry.finish(delivery, err) {
			if err != nil {
				f.abort(entry, err, nil)
			}
			return
		}
	}
}

func (e *fanoutEntry) enqueue(frame fanoutFrame) (accepted, full bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return false, false
	}
	if e.lagFrames >= e.maxFrames || e.lagFrames > 0 && e.lagBytes+len(frame.data) > e.maxBytes {
		return false, true
	}
	if e.queued == len(e.queue) {
		e.grow()
	}
	e.queue[(e.head+e.queued)%len(e.queue)] = fanoutFrame{json: frame.json, data: bytes.Clone(frame.data)}
	e.queued++
	e.lagFrames++
	e.lagBytes += len(frame.data)
	select {
	case e.wake <- struct{}{}:
	default:
	}
	return true, false
}

// grow doubles the ring, never beyond maxFrames.
func (e *fanoutEntry) grow() {
	size := 2 * len(e.queue)
	if size == 0 {
		size = 1
	}
	if size > e.maxFrames {
		size = e.maxFrames
	}
	grown := make([]fanoutFrame, size)
	for i := range e.queued {
		grown[i] = e.queue[(e.head+i)%len(e.queue)]
	}
	e.queue = grown
	e.head = 0
}

// retainedLocked reports the queued frames and their combined size.
func (e *fanoutEntry) retainedLocked() (count, size int) {
	count = e.queued
	for i := range e.queued {
		size += len(e.queue[(e.head+i)%len(e.queue)].data)
	}
	return count, size
}

func (e *fanoutEntry) next() (fanoutFrame, bool) {
	for {
		e.mu.Lock()
		if e.stopped {
			e.mu.Unlock()
			return fanoutFrame{}, false
		}
		if e.queued > 0 {
			delivery := e.queue[e.head]
			e.queue[e.head] = fanoutFrame{}
			e.head = (e.head + 1) % len(e.queue)
			e.queued--
			e.inFlight = true
			e.mu.Unlock()
			return delivery, true
		}
		e.mu.Unlock()
		select {
		case <-e.wake:
		case <-e.done:
			return fanoutFrame{}, false
		}
	}
}

func (e *fanoutEntry) finish(delivery fanoutFrame, sendErr error) bool {
	e.mu.Lock()
	if e.inFlight {
		e.lagFrames--
		e.lagBytes -= len(delivery.data)
		e.inFlight = false
	}
	active := !e.stopped
	if sendErr != nil && active {
		e.droppedFrames++
		e.droppedBytes += uint64(len(delivery.data))
	}
	e.mu.Unlock()
	select {
	case e.space <- struct{}{}:
	default:
	}
	return sendErr == nil || !active
}

func (e *fanoutEntry) stop() {
	e.stopOnce.Do(func() {
		e.mu.Lock()
		waiting, waitingBytes := e.retainedLocked()
		e.queue = nil
		e.head = 0
		e.queued = 0
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
