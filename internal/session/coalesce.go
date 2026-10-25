package session

import (
	"sync"
	"time"
)

const (
	outputCoalesceInterval = 8 * time.Millisecond
	outputCoalesceMax      = 64 << 10
	// outputCoalesceIdleGrace replaces the full window for the first chunk after the coalescer has been idle for at least one window:
	// interactive echo is not held back 8 ms, while data arriving within the grace is still merged into the same frame.
	outputCoalesceIdleGrace = time.Millisecond
)

// outCoalescer is the subscriber-side output coalescing buffer: a first chunk at the limit is handed to the subscriber's fanout directly,
// otherwise a short window accumulates; when the window fires or the buffer reaches the limit, the whole batch is enqueued without truncation.
// Batch extraction and fanout enqueue (feed and timer flush) go through feedGate, preserving order with attach replay/snapshot frames. The
// fanout worker owns the actual blocking send and its cancellation/replay recovery.
type outCoalescer struct {
	mu      sync.Mutex
	buf     []byte
	timer   *time.Timer
	gone    bool
	onTimer func()
	// lastFlush is when the last batch left the coalescer (zero if none); now is injectable for tests.
	lastFlush time.Time
	now       func() time.Time
}

func newOutCoalescer(onTimer func()) *outCoalescer {
	return &outCoalescer{onTimer: onTimer, now: time.Now}
}

// add appends data and returns the batch to enqueue immediately (first chunk over the limit, or the whole buffer at the limit);
// below the limit it buffers and starts the window timer, returning sendNow=false.
func (c *outCoalescer) add(data []byte) (batch []byte, sendNow bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone {
		return nil, false
	}
	if len(c.buf) == 0 && len(data) >= outputCoalesceMax {
		c.lastFlush = c.now()
		return data, true
	}
	wasEmpty := len(c.buf) == 0
	c.buf = append(c.buf, data...)
	if len(c.buf) >= outputCoalesceMax {
		batch := c.buf
		c.buf = nil
		c.lastFlush = c.now()
		if c.timer != nil {
			c.timer.Stop()
			c.timer = nil
		}
		return batch, true
	}
	if c.timer == nil {
		c.timer = time.AfterFunc(c.windowLocked(wasEmpty), c.onTimer)
	}
	return nil, false
}

// windowLocked picks the trailing window: the short idle grace for the first chunk after at least one idle window, otherwise the full window.
func (c *outCoalescer) windowLocked(wasEmpty bool) time.Duration {
	if wasEmpty && (c.lastFlush.IsZero() || c.now().Sub(c.lastFlush) >= outputCoalesceInterval) {
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
	c.lastFlush = c.now()
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
