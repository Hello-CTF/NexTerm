package base

import (
	"context"
	"io"
	"sync"
)

const outputQueueEvents = 32

type OutputEvent struct {
	Data     []byte
	Stderr   bool
	Sequence uint64
	Err      error
}

type OrderedOutput interface {
	NextOutput(ctx context.Context) (OutputEvent, error)
}

type outputMode uint32

const (
	outputModeUnset outputMode = iota
	outputModeRaw
	outputModeOrdered
)

type outputEvent struct {
	event  OutputEvent
	offset int
}

type outputQueue struct {
	events []outputEvent
}

func (q *outputQueue) full() bool {
	return len(q.events) >= outputQueueEvents
}

func (q *outputQueue) append(event OutputEvent) {
	q.events = append(q.events, outputEvent{event: event})
}

func (q *outputQueue) pop() OutputEvent {
	event := q.events[0].event
	q.events = q.events[1:]
	return event
}

func (q *outputQueue) read(buffer []byte) int {
	first := &q.events[0]
	count := copy(buffer, first.event.Data[first.offset:])
	first.offset += count
	if first.offset == len(first.event.Data) {
		q.events = q.events[1:]
	}
	return count
}

type OutputRouter struct {
	ctx       context.Context
	mu        sync.Mutex
	mode      outputMode
	sequence  uint64
	stdout    outputQueue
	stderr    outputQueue
	ordered   outputQueue
	closed    bool
	closedCh  chan struct{}
	notify    chan struct{}
	closeOnce sync.Once
}

func NewOutputRouter(ctx context.Context) *OutputRouter {
	router := &OutputRouter{
		ctx:      ctx,
		closedCh: make(chan struct{}),
		notify:   make(chan struct{}, 1),
	}
	context.AfterFunc(ctx, router.Close)
	return router
}

func (r *OutputRouter) Writer(stderr bool) io.Writer {
	return &outputRouterWriter{router: r, stderr: stderr}
}

func (r *OutputRouter) Stderr() io.Reader {
	return &outputRouterReader{router: r}
}

func (r *OutputRouter) ReadStdout(buffer []byte) (int, error) {
	return r.readRaw(buffer, false)
}

func (r *OutputRouter) NextOutput(ctx context.Context) (OutputEvent, error) {
	for {
		r.mu.Lock()
		if err := r.selectModeLocked(outputModeOrdered); err != nil {
			r.mu.Unlock()
			return OutputEvent{}, err
		}
		if len(r.ordered.events) > 0 {
			event := r.ordered.pop()
			r.signalLocked()
			r.mu.Unlock()
			return event, nil
		}
		if r.closed {
			r.mu.Unlock()
			return OutputEvent{}, io.EOF
		}
		notify, closed := r.notify, r.closedCh
		r.mu.Unlock()
		select {
		case <-notify:
		case <-closed:
		case <-ctx.Done():
			return OutputEvent{}, ctx.Err()
		case <-r.ctx.Done():
		}
	}
}

func (r *OutputRouter) Close() {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		close(r.closedCh)
		r.signalLocked()
		r.mu.Unlock()
	})
}

func (r *OutputRouter) write(data []byte, stderr bool) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	data = append([]byte(nil), data...)
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return 0, ErrClosed
		}
		queues := r.writeQueuesLocked(stderr)
		capacity := true
		for _, queue := range queues {
			capacity = capacity && !queue.full()
		}
		if capacity {
			r.sequence++
			event := OutputEvent{Data: data, Stderr: stderr, Sequence: r.sequence}
			for _, queue := range queues {
				queue.append(event)
			}
			r.signalLocked()
			r.mu.Unlock()
			return len(data), nil
		}
		notify, closed := r.notify, r.closedCh
		r.mu.Unlock()
		select {
		case <-notify:
		case <-closed:
			return 0, ErrClosed
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	}
}

func (r *OutputRouter) writeQueuesLocked(stderr bool) []*outputQueue {
	switch r.mode {
	case outputModeOrdered:
		return []*outputQueue{&r.ordered}
	case outputModeRaw:
		if stderr {
			return []*outputQueue{&r.stderr}
		}
		return []*outputQueue{&r.stdout}
	default:
		if stderr {
			return []*outputQueue{&r.stderr, &r.ordered}
		}
		return []*outputQueue{&r.stdout, &r.ordered}
	}
}

func (r *OutputRouter) readRaw(buffer []byte, stderr bool) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	for {
		r.mu.Lock()
		if err := r.selectModeLocked(outputModeRaw); err != nil {
			r.mu.Unlock()
			return 0, err
		}
		queue := &r.stdout
		if stderr {
			queue = &r.stderr
		}
		if len(queue.events) > 0 {
			count := queue.read(buffer)
			r.signalLocked()
			r.mu.Unlock()
			return count, nil
		}
		if r.closed {
			r.mu.Unlock()
			return 0, io.EOF
		}
		notify, closed := r.notify, r.closedCh
		r.mu.Unlock()
		select {
		case <-notify:
		case <-closed:
		case <-r.ctx.Done():
		}
	}
}

func (r *OutputRouter) selectModeLocked(mode outputMode) error {
	if r.mode == outputModeUnset {
		r.mode = mode
		if mode == outputModeRaw {
			r.ordered.events = nil
		} else {
			r.stdout.events = nil
			r.stderr.events = nil
		}
		r.signalLocked()
		return nil
	}
	if r.mode != mode {
		return ErrOutputMode
	}
	return nil
}

func (r *OutputRouter) signalLocked() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

type outputRouterWriter struct {
	router *OutputRouter
	stderr bool
}

func (w *outputRouterWriter) Write(data []byte) (int, error) {
	return w.router.write(data, w.stderr)
}

type outputRouterReader struct {
	router *OutputRouter
}

func (r *outputRouterReader) Read(buffer []byte) (int, error) {
	return r.router.readRaw(buffer, true)
}
