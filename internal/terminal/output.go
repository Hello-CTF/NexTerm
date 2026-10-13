package terminal

import (
	"context"
	"sync"
	"time"
)

const (
	HiddenFlushInterval = 50 * time.Millisecond
	HiddenBatchMax      = 1024 * 1024
)

type Output struct {
	tab    *Tab
	fanout *Fanout

	chunkMu     sync.Mutex
	pending     []byte
	deadline    time.Time
	hasDeadline bool

	wake        chan struct{}
	done        chan struct{}
	flusherDone chan struct{}
	closeOnce   sync.Once
	flushCtx    context.Context
	cancelFlush context.CancelFunc
}

func NewOutput(tab *Tab) *Output {
	flushCtx, cancelFlush := context.WithCancel(context.Background())
	o := &Output{
		tab:         tab,
		fanout:      NewFanout(),
		wake:        make(chan struct{}, 1),
		done:        make(chan struct{}),
		flusherDone: make(chan struct{}),
		flushCtx:    flushCtx,
		cancelFlush: cancelFlush,
	}
	go o.flushLoop()
	return o
}

func (o *Output) SubscriberCount() int { return o.fanout.Count() }

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

func (o *Output) Chunk(ctx context.Context, chunk []byte) error {
	if len(chunk) == 0 {
		return nil
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

func (o *Output) Flush(ctx context.Context) {
	o.chunkMu.Lock()
	defer o.chunkMu.Unlock()
	o.flushLocked(ctx)
}

func (o *Output) Attach(id string, sink Sink) {
	o.chunkMu.Lock()
	defer o.chunkMu.Unlock()
	o.fanout.Attach(id, sink)
}

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

func (o *Output) Detach(id string) bool { return o.fanout.Detach(id) }

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

func (o *Output) sendLocked(ctx context.Context, frame []byte) {
	if len(frame) == 0 {
		return
	}
	o.fanout.Send(ctx, frame)
}

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
