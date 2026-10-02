package terminal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// HiddenFlushInterval is the batching delay for hidden tabs.
	HiddenFlushInterval = 50 * time.Millisecond
	// HiddenBatchMax flushes a hidden batch immediately above this size.
	HiddenBatchMax = 1024 * 1024
	// ReadChunk is the pump read buffer size.
	ReadChunk = 64 * 1024
	// QueueMessages bounds the pump's read queue; with ReadChunk this
	// gives 4 MiB of end-to-end pressure before the reader blocks.
	QueueMessages = 64
)

// Output coordinates one tab's live output path: Feed -> record -> fan-out,
// with visible/hidden batching, throttling and atomic replay-attach.
// All live output should go through Output; direct Tab.Feed calls update
// only the tab state (used for injected banners and tests).
type Output struct {
	tab      *Tab
	fanout   *Fanout
	inflight *Inflight

	onThrottle atomic.Value // of throttleHandler

	chunkMu     sync.Mutex // serializes chunks and replay-attach
	pending     []byte
	deadline    time.Time
	hasDeadline bool

	throttled atomic.Bool

	wake        chan struct{}
	done        chan struct{}
	flusherDone chan struct{}
	closeOnce   sync.Once
	flushCtx    context.Context
	cancelFlush context.CancelFunc
}

type throttleHandler struct{ fn func(inflight int64) }

// OutputOption customizes NewOutput.
type OutputOption func(*Output)

// WithInflight shares an Inflight counter with the caller (pumps Add on
// read; Output subtracts after fan-out).
func WithInflight(counter *Inflight) OutputOption {
	return func(o *Output) {
		if counter != nil {
			o.inflight = counter
		}
	}
}

// WithThrottleHandler installs a callback fired once per throttling
// episode when inflight exceeds InflightPause (reset at a quarter of the
// threshold).
func WithThrottleHandler(h func(inflight int64)) OutputOption {
	return func(o *Output) {
		if h != nil {
			o.onThrottle.Store(throttleHandler{fn: h})
		}
	}
}

// NewOutput creates the output pipeline for tab and starts its hidden
// batch flusher. Call Close to release it.
func NewOutput(tab *Tab, opts ...OutputOption) *Output {
	flushCtx, cancelFlush := context.WithCancel(context.Background())
	o := &Output{
		tab:         tab,
		fanout:      NewFanout(),
		inflight:    NewInflight(),
		wake:        make(chan struct{}, 1),
		done:        make(chan struct{}),
		flusherDone: make(chan struct{}),
		flushCtx:    flushCtx,
		cancelFlush: cancelFlush,
	}
	for _, opt := range opts {
		opt(o)
	}
	go o.flushLoop()
	return o
}

// Tab returns the pipeline's tab.
func (o *Output) Tab() *Tab { return o.tab }

// Fanout returns the subscriber set.
func (o *Output) Fanout() *Fanout { return o.fanout }

// Inflight returns the backpressure counter.
func (o *Output) Inflight() *Inflight { return o.inflight }

// SubscriberCount returns the number of attached channels.
func (o *Output) SubscriberCount() int { return o.fanout.Count() }

// SetVisible updates tab visibility; becoming visible schedules an
// immediate flush of any hidden batch so the client catches up at once.
func (o *Output) SetVisible(visible bool) {
	o.tab.SetVisible(visible)
	if visible {
		o.chunkMu.Lock()
		if len(o.pending) > 0 {
			o.deadline = time.Now()
			o.hasDeadline = true
		}
		o.chunkMu.Unlock()
		o.signalWake()
	}
}

// Chunk ingests one raw output chunk. It blocks (honoring ctx) while
// inflight exceeds InflightPause, then feeds, records and fans out:
// immediately when the tab is visible, batched when hidden.
func (o *Output) Chunk(ctx context.Context, chunk []byte) error {
	if len(chunk) == 0 {
		return nil
	}
	if err := o.waitThrottle(ctx); err != nil {
		return err
	}
	o.chunkMu.Lock()
	defer o.chunkMu.Unlock()
	o.tab.Feed(chunk)
	o.tab.recordChunk(chunk)
	if o.tab.IsVisible() {
		o.flushLocked(ctx)
		o.sendLocked(ctx, chunk)
		return nil
	}
	o.pending = append(o.pending, chunk...)
	if !o.hasDeadline {
		o.deadline = time.Now().Add(HiddenFlushInterval)
		o.hasDeadline = true
		o.signalWake()
	}
	if len(o.pending) > HiddenBatchMax {
		o.flushLocked(ctx)
	}
	return nil
}

// Flush delivers any buffered hidden batch now.
func (o *Output) Flush(ctx context.Context) {
	o.chunkMu.Lock()
	defer o.chunkMu.Unlock()
	o.flushLocked(ctx)
}

// Attach subscribes sink without replay.
func (o *Output) Attach(id string, sink Sink) {
	o.chunkMu.Lock()
	defer o.chunkMu.Unlock()
	o.fanout.Attach(id, sink)
}

// AttachReplay atomically replays up to replayBytes of raw scrollback to
// sink and subscribes it: relative to live chunks, there is neither a gap
// nor a duplicate between the replayed tail and the live stream.
// A sink that fails the replay send is not attached.
func (o *Output) AttachReplay(ctx context.Context, id string, sink Sink, replayBytes int) error {
	o.chunkMu.Lock()
	defer o.chunkMu.Unlock()
	replay := o.tab.Dump(replayBytes)
	if len(replay) > 0 {
		if err := sink.SendBinary(ctx, replay); err != nil {
			return err
		}
	}
	o.fanout.Attach(id, sink)
	return nil
}

// AttachFrom replays from absolute sequence seq (clamped to the retained
// window), at most max bytes per call. It attaches the sink for live
// frames only when the replay reached the current head; otherwise it
// returns attached=false and the resume sequence, and the sink is NOT
// attached: attaching after a truncated replay would silently skip the
// bytes between the replay end and the live head. Call again with the
// returned sequence to continue until attached=true, or use AttachReplay
// for a tail-only replay. Ring.ReplayFrom remains available for paged
// reads.
func (o *Output) AttachFrom(ctx context.Context, id string, sink Sink, seq uint64, max int) (next uint64, attached bool, err error) {
	o.chunkMu.Lock()
	defer o.chunkMu.Unlock()
	replay, start := o.tab.ReplayFrom(seq, max)
	if len(replay) > 0 {
		if err := sink.SendBinary(ctx, replay); err != nil {
			return start, false, err
		}
	}
	next = start + uint64(len(replay))
	if next < o.tab.LatestSeq() {
		return next, false, nil
	}
	o.fanout.Attach(id, sink)
	return next, true, nil
}

// Detach removes one subscriber.
func (o *Output) Detach(id string) bool { return o.fanout.Detach(id) }

// DetachAll removes every subscriber.
func (o *Output) DetachAll() int { return o.fanout.DetachAll() }

// Close stops the hidden flusher. It is idempotent and does not close the
// tab or flush; callers ending a stream should Flush first (Consume and
// PumpReader already do). Close cancels the flusher's context, so even a
// blocked sink send cannot keep Close waiting forever.
func (o *Output) Close() {
	o.closeOnce.Do(func() {
		close(o.done)
		o.cancelFlush()
		<-o.flusherDone
	})
}

func (o *Output) signalWake() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (o *Output) waitThrottle(ctx context.Context) error {
	if o.inflight.Load() <= InflightPause {
		if o.inflight.Load() <= InflightPause/4 {
			o.throttled.Store(false)
		}
		return nil
	}
	if o.throttled.CompareAndSwap(false, true) {
		if h, _ := o.onThrottle.Load().(throttleHandler); h.fn != nil {
			h.fn(o.inflight.Load())
		}
	}
	if err := o.inflight.WaitBelow(ctx, InflightPause); err != nil {
		return err
	}
	if o.inflight.Load() <= InflightPause/4 {
		o.throttled.Store(false)
	}
	return nil
}

// sendLocked fans one frame out and settles its inflight accounting.
func (o *Output) sendLocked(ctx context.Context, frame []byte) {
	if len(frame) == 0 {
		return
	}
	o.fanout.Send(ctx, frame)
	o.inflight.SubSaturating(int64(len(frame)))
}

// flushLocked sends the hidden batch, if any, and clears the deadline.
func (o *Output) flushLocked(ctx context.Context) {
	if len(o.pending) == 0 {
		o.hasDeadline = false
		return
	}
	batch := o.pending
	o.pending = nil
	o.hasDeadline = false
	o.sendLocked(ctx, batch)
}

// flushLoop fires hidden batches at their deadline; woken early on new
// deadlines, visibility changes or Close.
func (o *Output) flushLoop() {
	defer close(o.flusherDone)
	for {
		o.chunkMu.Lock()
		deadline, has := o.deadline, o.hasDeadline
		o.chunkMu.Unlock()

		var timer *time.Timer
		var timerC <-chan time.Time
		if has {
			timer = time.NewTimer(time.Until(deadline))
			timerC = timer.C
		}
		select {
		case <-o.done:
			if timer != nil {
				timer.Stop()
			}
			return
		case <-o.wake:
			if timer != nil {
				timer.Stop()
			}
		case <-timerC:
			o.Flush(o.flushCtx)
		}
	}
}

// Consume pulls chunks until the channel closes or ctx is done.
//
// Flush semantics differ by ending: on normal EOF (channel closed) the
// remaining hidden batch is flushed with a non-canceled context so the
// tail is delivered. On cancellation or a Chunk error there is no final
// flush: the stream is ending anyway, the raw bytes remain in the ring
// for replay, and a sink blocking its send can never deadlock the
// cancellation path. A nil return means the channel closed.
func (o *Output) Consume(ctx context.Context, chunks <-chan []byte) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case chunk, ok := <-chunks:
			if !ok {
				o.Flush(context.WithoutCancel(ctx))
				return nil
			}
			if err := o.Chunk(ctx, chunk); err != nil {
				return err
			}
		}
	}
}

// PumpReader drives a blocking io.Reader (local PTY) through Output: a
// reader goroutine with a bounded queue provides end-to-end backpressure.
// It returns on EOF (nil), on a read error, or when ctx is done.
// Canceling ctx does not interrupt a blocked Read; close r to unblock the
// reader goroutine.
func PumpReader(ctx context.Context, r io.Reader, o *Output) error {
	chunks := make(chan []byte, QueueMessages)
	var readErr atomic.Value // of error
	go func() {
		defer close(chunks)
		buf := make([]byte, ReadChunk)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				o.inflight.Add(int64(n))
				data := bytes.Clone(buf[:n])
				select {
				case chunks <- data:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					readErr.Store(err)
				}
				return
			}
		}
	}()
	if err := o.Consume(ctx, chunks); err != nil {
		return err
	}
	if err, _ := readErr.Load().(error); err != nil {
		return err
	}
	return nil
}
