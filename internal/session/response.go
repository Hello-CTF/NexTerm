package session

import (
	"sync"
	"sync/atomic"
)

type terminalResponse struct {
	generation uint64
	data       []byte
}

type responseQueue struct {
	mu         sync.Mutex
	items      []terminalResponse
	wake       chan struct{}
	closed     atomic.Bool
	generation atomic.Uint64
}

func newResponseQueue(generation uint64) *responseQueue {
	queue := &responseQueue{wake: make(chan struct{}, 1)}
	queue.generation.Store(generation)
	return queue
}

func (q *responseQueue) enqueue(data []byte) {
	if q.closed.Load() || len(data) == 0 {
		return
	}
	q.mu.Lock()
	if !q.closed.Load() {
		q.items = append(q.items, terminalResponse{generation: q.generation.Load(), data: append([]byte(nil), data...)})
	}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *responseQueue) close() {
	q.closed.Store(true)
	q.mu.Lock()
	q.items = nil
	q.mu.Unlock()
}

func (q *responseQueue) next() (terminalResponse, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return terminalResponse{}, false
	}
	item := q.items[0]
	q.items[0] = terminalResponse{}
	q.items = q.items[1:]
	if len(q.items) == 0 {
		q.items = nil
	}
	return item, true
}

func (t *Tab) setGenerationLocked(generation uint64) {
	t.generation = generation
	if t.responses != nil {
		t.responses.generation.Store(generation)
	}
}

func (m *Manager) startResponses(tab *Tab) bool {
	return m.startGoroutine(func() {
		for {
			if item, ok := tab.responses.next(); ok {
				tab.mu.Lock()
				channel := tab.channel
				current := !tab.closed && tab.generation == item.generation
				tab.mu.Unlock()
				if current && channel != nil {
					tab.writeMu.Lock()
					_ = writeAll(channel, item.data)
					tab.writeMu.Unlock()
				}
				continue
			}
			select {
			case <-tab.ctx.Done():
				return
			case <-tab.responses.wake:
			}
		}
	})
}
