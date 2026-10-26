package session

import (
	"sync"
	"time"
)

const (
	outputCoalesceInterval = 8 * time.Millisecond
	outputCoalesceMax      = 64 << 10
	// The first chunk after the coalescer has been idle for at least one window leaves immediately, so interactive echo is not held
	// back by a timer (whose wake-up alone adds one to several ms). outputCoalesceIdleGrace is the window for the chunk that follows
	// it: the rest of a multi-write redraw is still merged into one frame shortly after, not one full window later.
	outputCoalesceIdleGrace = time.Millisecond

	// Bulk mode trades latency for bandwidth under sustained heavy output (cat of a large file, build logs): fewer, larger frames compress better
	// with permessage-deflate/zstd and cost fewer WebSocket/TCP headers. Interactive echo and small bursts never enter it, so their latency is unchanged.
	//
	// outputCoalesceBulkInterval is 4x the normal window; the extra delay is bounded by 32 ms, below what a human perceives while a screen is scrolling.
	outputCoalesceBulkInterval = 4 * outputCoalesceInterval
	// outputCoalesceBulkMax is the frame cap in bulk mode, 4x the normal cap; it bounds per-frame memory and head-of-line delay on the wire.
	outputCoalesceBulkMax = 4 * outputCoalesceMax
	// outputCoalesceBulkEnterBytes is the size at which a flush counts as heavy (half the normal cap, about 4 MB/s over a normal window).
	outputCoalesceBulkEnterBytes = outputCoalesceMax / 2
	// outputCoalesceBulkEnterFlushes is how many consecutive heavy flushes, each within outputCoalesceBulkGap of the previous one, enter bulk mode.
	// Three keeps a single large paste or screen redraw from widening the window.
	outputCoalesceBulkEnterFlushes = 3
	// outputCoalesceBulkGap is the longest gap between flushes that still counts as a sustained stream while entering bulk mode.
	outputCoalesceBulkGap = 2 * outputCoalesceInterval
	// outputCoalesceBulkExitBytes: a bulk flush smaller than this (under 0.5 MB/s over a bulk window) leaves bulk mode.
	outputCoalesceBulkExitBytes = outputCoalesceMax / 4
	// outputCoalesceBulkIdle: output pausing this long leaves bulk mode, so the next chunk gets the idle grace again.
	outputCoalesceBulkIdle = 2 * outputCoalesceInterval
)

// stopper is the cancel half of a window timer; tests substitute a deterministic clock.
type stopper interface{ Stop() bool }

// outCoalescer is the subscriber-side output coalescing buffer: a first chunk at the limit is handed to the subscriber's fanout directly,
// otherwise a short window accumulates; when the window fires or the buffer reaches the limit, the whole batch is enqueued without truncation.
// Batch extraction and fanout enqueue (feed and timer flush) go through feedGate, preserving order with attach replay/snapshot frames. The
// fanout worker owns the actual blocking send and its cancellation/replay recovery.
type outCoalescer struct {
	mu      sync.Mutex
	buf     []byte
	timer   stopper
	gone    bool
	onTimer func()
	// lastFlush is when the last batch left the coalescer (zero if none); now is injectable for tests.
	lastFlush time.Time
	now       func() time.Time
	afterFunc func(d time.Duration, f func()) stopper
	// bulk is the adaptive wide-window state; heavy counts consecutive heavy flushes while entering it. fixed disables adaptation (tests, baseline).
	bulk  bool
	heavy int
	fixed bool
	// followUp is set when the last batch left immediately after idle; the next window is the idle grace.
	followUp bool
}

func newOutCoalescer(onTimer func()) *outCoalescer {
	return &outCoalescer{
		onTimer:   onTimer,
		now:       time.Now,
		afterFunc: func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) },
	}
}

func (c *outCoalescer) maxLocked() int {
	if c.bulk {
		return outputCoalesceBulkMax
	}
	return outputCoalesceMax
}

// flushedLocked records a departing batch and updates bulk mode: consecutive heavy flushes enter it, a light flush leaves it.
func (c *outCoalescer) flushedLocked(n int) {
	c.followUp = false
	now := c.now()
	prev := c.lastFlush
	c.lastFlush = now
	if c.fixed {
		return
	}
	if c.bulk {
		if n < outputCoalesceBulkExitBytes {
			c.bulk, c.heavy = false, 0
		}
		return
	}
	if n < outputCoalesceBulkEnterBytes || (!prev.IsZero() && now.Sub(prev) > outputCoalesceBulkGap) {
		c.heavy = 0
		if n < outputCoalesceBulkEnterBytes {
			return
		}
	}
	c.heavy++
	if c.heavy >= outputCoalesceBulkEnterFlushes {
		c.bulk = true
	}
}

// noteInput drops out of bulk mode when user input arrives so the echo is not held for a wide window; a pending wide window is re-armed with the idle grace.
func (c *outCoalescer) noteInput() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.heavy = 0
	if !c.bulk || c.gone {
		return
	}
	c.bulk = false
	if c.timer != nil && c.timer.Stop() {
		c.timer = c.afterFunc(outputCoalesceIdleGrace, c.onTimer)
	}
}

// add appends data and returns the batch to enqueue immediately (first chunk over the limit, or the whole buffer at the limit);
// below the limit it buffers and starts the window timer, returning sendNow=false.
func (c *outCoalescer) add(data []byte) (batch []byte, sendNow bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone {
		return nil, false
	}
	if c.bulk && c.timer == nil && len(c.buf) == 0 && c.now().Sub(c.lastFlush) >= outputCoalesceBulkIdle {
		c.bulk, c.heavy = false, 0
	}
	limit := c.maxLocked()
	if len(c.buf) == 0 && len(data) >= limit {
		c.flushedLocked(len(data))
		return data, true
	}
	if len(c.buf) == 0 && !c.bulk && (c.lastFlush.IsZero() || c.now().Sub(c.lastFlush) >= outputCoalesceInterval) {
		c.flushedLocked(len(data))
		c.followUp = true
		return data, true
	}
	wasEmpty := len(c.buf) == 0
	c.buf = append(c.buf, data...)
	if len(c.buf) >= limit {
		batch := c.buf
		c.buf = nil
		c.flushedLocked(len(batch))
		if c.timer != nil {
			c.timer.Stop()
			c.timer = nil
		}
		return batch, true
	}
	if c.timer == nil {
		c.timer = c.afterFunc(c.windowLocked(wasEmpty), c.onTimer)
	}
	return nil, false
}

// windowLocked picks the trailing window: the short idle grace for the chunk right after an immediate idle send, otherwise the full window.
func (c *outCoalescer) windowLocked(wasEmpty bool) time.Duration {
	if c.bulk {
		return outputCoalesceBulkInterval
	}
	if wasEmpty && c.followUp {
		return outputCoalesceIdleGrace
	}
	return outputCoalesceInterval
}

// take removes the buffered batch when the window fires, for the timer flush to enqueue.
func (c *outCoalescer) take() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timer = nil
	if c.gone || len(c.buf) == 0 {
		return nil
	}
	batch := c.buf
	c.buf = nil
	c.flushedLocked(len(batch))
	return batch
}

// addPaused appends output for a paused subscriber: no limit check, no window
// timer, no flush. The buffer is bounded by the pump stall (outputStalled)
// instead of the coalescing limit, so a paused channel neither drops nor
// grows without bound while the PTY read is blocked.
func (c *outCoalescer) addPaused(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone {
		return
	}
	c.buf = append(c.buf, data...)
}

func (c *outCoalescer) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.buf)
}

// teardown stops the window and discards all residual data. Recovery starts at the client's last continuous anchor;
// writing a newer suffix here could advance that anchor across older frames discarded by the fanout.
func (c *outCoalescer) teardown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gone = true
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.buf = nil
}
