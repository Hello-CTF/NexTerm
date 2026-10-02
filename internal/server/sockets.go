package server

import (
	"context"
	"errors"
	"sync"
)

var errServerClosed = errors.New("server is closed")

type socketTracker struct {
	mu      sync.Mutex
	nextID  uint64
	cancels map[uint64]context.CancelFunc
	closed  bool
	wg      sync.WaitGroup
}

func (t *socketTracker) track(parent context.Context) (context.Context, func(), error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, nil, errServerClosed
	}
	t.nextID++
	id := t.nextID
	ctx, cancel := context.WithCancel(parent)
	if t.cancels == nil {
		t.cancels = make(map[uint64]context.CancelFunc)
	}
	t.cancels[id] = cancel
	t.wg.Add(1)
	t.mu.Unlock()

	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel()
			t.mu.Lock()
			delete(t.cancels, id)
			t.mu.Unlock()
			t.wg.Done()
		})
	}, nil
}

func (t *socketTracker) closeAndWait() {
	t.mu.Lock()
	t.closed = true
	cancels := make([]context.CancelFunc, 0, len(t.cancels))
	for _, cancel := range t.cancels {
		cancels = append(cancels, cancel)
	}
	t.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	t.wg.Wait()
}
