package session

import (
	"sync"
	"time"
)

const (
	outputCoalesceInterval = 8 * time.Millisecond
	outputCoalesceMax      = 64 << 10
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
}

func newOutCoalescer(onTimer func()) *outCoalescer {
	return &outCoalescer{onTimer: onTimer}
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
		return data, true
	}
	c.buf = append(c.buf, data...)
	if len(c.buf) >= outputCoalesceMax {
		batch := c.buf
		c.buf = nil
		if c.timer != nil {
			c.timer.Stop()
			c.timer = nil
		}
		return batch, true
	}
	if c.timer == nil {
		c.timer = time.AfterFunc(outputCoalesceInterval, c.onTimer)
	}
	return nil, false
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
