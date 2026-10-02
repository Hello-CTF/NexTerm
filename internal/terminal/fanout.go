package terminal

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
)

// Backpressure thresholds, in bytes of read-but-not-yet-fanned-out data.
const (
	// InflightPause pauses pumps once inflight exceeds it.
	InflightPause = 4 * 1024 * 1024
	// InflightDrop is the per-client pending-bytes ceiling transports
	// should apply before dropping a hopelessly slow client.
	InflightDrop = 16 * 1024 * 1024
)

// Sink is one subscribed output channel. It matches ipc.BinaryStream's
// send side, so stream adapters plug in directly. SendBinary must honor
// ctx cancellation; implementations that retain data after returning own
// the passed slice (Fanout never reuses a slice it handed out) but must
// not modify it.
type Sink interface {
	SendBinary(ctx context.Context, data []byte) error
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(ctx context.Context, data []byte) error

// SendBinary implements Sink.
func (f SinkFunc) SendBinary(ctx context.Context, data []byte) error { return f(ctx, data) }

type fanoutEntry struct {
	sink Sink
	gen  uint64
}

// Fanout delivers output frames to every subscriber with a global,
// consistent order: Send calls are serialized, so every client observes
// the same frame sequence. A failing sink is detached in place (a dead
// channel would otherwise be retried on every frame). With no subscribers
// Send is a no-op returning false: bytes stay in the ring for later
// replay, which is the normal "browser closed" state.
//
// Fanout itself does not queue; backpressure is provided by Inflight and
// the sinks' own bounded queues.
type Fanout struct {
	mu     sync.Mutex
	sendMu sync.Mutex
	next   uint64
	sinks  map[string]fanoutEntry
}

// NewFanout creates an empty fan-out set.
func NewFanout() *Fanout {
	return &Fanout{sinks: make(map[string]fanoutEntry)}
}

// Attach adds or replaces the sink for id. Reusing an id (page refresh,
// reconnect) replaces the stale sink instead of accumulating dead ones.
func (f *Fanout) Attach(id string, sink Sink) (replaced bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	_, replaced = f.sinks[id]
	f.sinks[id] = fanoutEntry{sink: sink, gen: f.next}
	return replaced
}

// Detach removes one sink.
func (f *Fanout) Detach(id string) (removed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sinks[id]; ok {
		delete(f.sinks, id)
		return true
	}
	return false
}

// DetachAll removes every sink and returns how many were removed.
func (f *Fanout) DetachAll() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.sinks)
	f.sinks = make(map[string]fanoutEntry)
	return n
}

// Count returns the number of attached sinks (channels, not devices).
func (f *Fanout) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sinks)
}

func (f *Fanout) detachIf(id string, gen uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if entry, ok := f.sinks[id]; ok && entry.gen == gen {
		delete(f.sinks, id)
	}
}

// Send delivers one frame to every attached sink. Delivery order across
// different clients is unspecified, but every retained client observes
// the same complete frame sequence: a sink whose send fails is detached,
// and sinks skipped because ctx was canceled are detached as well. An
// interrupted client must therefore re-attach (replay rebuilds the
// missing bytes) instead of silently continuing with a gap. The first
// successful sink receives data itself; additional sinks receive copies,
// so a retaining sink never aliases another sink's buffer.
//
// It returns true when at least one sink accepted the frame.
func (f *Fanout) Send(ctx context.Context, data []byte) bool {
	f.sendMu.Lock()
	defer f.sendMu.Unlock()

	f.mu.Lock()
	if len(f.sinks) == 0 {
		f.mu.Unlock()
		return false
	}
	type target struct {
		id   string
		gen  uint64
		sink Sink
	}
	targets := make([]target, 0, len(f.sinks))
	for id, entry := range f.sinks {
		targets = append(targets, target{id: id, gen: entry.gen, sink: entry.sink})
	}
	f.mu.Unlock()

	delivered := false
	for _, tgt := range targets {
		if ctx.Err() != nil {
			// The stream to this client was interrupted mid-frame
			// sequence; continuing later would leave a gap.
			f.detachIf(tgt.id, tgt.gen)
			continue
		}
		payload := data
		if delivered {
			payload = bytes.Clone(data)
		}
		if err := tgt.sink.SendBinary(ctx, payload); err != nil {
			f.detachIf(tgt.id, tgt.gen)
			continue
		}
		delivered = true
	}
	return delivered
}

// Inflight tracks bytes read from the peer but not yet handed to sinks.
// Producers Add on read; Output subtracts (saturating) after fan-out.
// Waiters are woken by closing a generation channel (broadcast), so any
// number of simultaneous waiters is released once the count drops.
type Inflight struct {
	v  atomic.Int64
	mu sync.Mutex
	ch chan struct{}
}

// NewInflight creates a zeroed counter.
func NewInflight() *Inflight {
	return &Inflight{ch: make(chan struct{})}
}

// Load returns the current in-flight byte count.
func (i *Inflight) Load() int64 { return i.v.Load() }

// Add increases the count by n.
func (i *Inflight) Add(n int64) { i.v.Add(n) }

// SubSaturating decreases the count by n, clamping at zero (push-style
// sources never Add, so naive subtraction would underflow).
func (i *Inflight) SubSaturating(n int64) {
	for {
		cur := i.v.Load()
		next := cur - n
		if next < 0 {
			next = 0
		}
		if i.v.CompareAndSwap(cur, next) {
			break
		}
	}
	i.mu.Lock()
	close(i.ch)
	i.ch = make(chan struct{})
	i.mu.Unlock()
}

// WaitBelow blocks until the count is at or below limit, or ctx is done.
// Wakeups are broadcast to every waiter; there is no polling.
func (i *Inflight) WaitBelow(ctx context.Context, limit int64) error {
	for i.v.Load() > limit {
		i.mu.Lock()
		ch := i.ch
		i.mu.Unlock()
		// Re-check after registering: a subtraction between the loop
		// condition and here already closed ch, so nothing is missed.
		if i.v.Load() <= limit {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
	return nil
}
