package session

import (
	"context"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
)

const (
	outputCoalesceInterval = 8 * time.Millisecond
	outputCoalesceMax      = 64 << 10
)

// outCoalescer is the subscriber-side output coalescing buffer: a first chunk at the limit is sent directly, otherwise a short window
// accumulates; when the window fires or the buffer reaches the limit, the whole batch goes out, and oversized batches are never truncated.
// All send paths (synchronous feed sends, timer flush) go through feedGate, preserving relative order with attach replay/snapshot frames;
// teardown's fallback flush uses TrySend and never blocks, giving up the fallback when a send is in flight to avoid reordering with it.
type outCoalescer struct {
	mu      sync.Mutex
	buf     []byte
	timer   *time.Timer
	sending bool
	gone    bool
	onTimer func()
}

func newOutCoalescer(onTimer func()) *outCoalescer {
	return &outCoalescer{onTimer: onTimer}
}

// add appends data and returns the batch to send immediately (first chunk over the limit, or the whole buffer at the limit);
// below the limit it buffers and starts the window timer, returning sendNow=false.
func (c *outCoalescer) add(data []byte) (batch []byte, sendNow bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone {
		return nil, false
	}
	if len(c.buf) == 0 && len(data) >= outputCoalesceMax {
		c.sending = true
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
		c.sending = true
		return batch, true
	}
	if c.timer == nil {
		c.timer = time.AfterFunc(outputCoalesceInterval, c.onTimer)
	}
	return nil, false
}

// take removes the buffered batch when the window fires, for the timer flush to send.
func (c *outCoalescer) take() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timer = nil
	if c.gone || len(c.buf) == 0 {
		return nil
	}
	batch := c.buf
	c.buf = nil
	c.sending = true
	return batch
}

func (c *outCoalescer) sent() {
	c.mu.Lock()
	c.sending = false
	c.mu.Unlock()
}

// teardown stops the window and marks the coalescer removed; with no send in flight, the residual buffer is TrySent as a best-effort fallback
// (dropped if the queue is full or the channel was already replaced, consistent with producer.Close's existing queue-drop semantics).
func (c *outCoalescer) teardown(producer *hub.Producer) {
	c.mu.Lock()
	c.gone = true
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	batch := c.buf
	c.buf = nil
	flush := !c.sending && len(batch) > 0
	c.mu.Unlock()
	if flush {
		_ = producer.TrySendBinary(context.Background(), batch)
	}
}
