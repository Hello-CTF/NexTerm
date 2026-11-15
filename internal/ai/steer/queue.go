package steer

import (
	"errors"
	"sync"

	"github.com/cloudwego/eino/schema"
)

var (
	ErrQueueFull   = errors.New("steering queue is full")
	ErrQueueClosed = errors.New("steering queue is closed")
)

type Queue struct {
	mu      sync.Mutex
	limit   int
	pending []*schema.Message
	closed  bool
}

func NewQueue(limit int) *Queue {
	if limit <= 0 {
		limit = 64
	}
	return &Queue{limit: limit}
}

func (q *Queue) Push(message *schema.Message) error {
	if message == nil {
		return errors.New("steering message is required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrQueueClosed
	}
	if len(q.pending) >= q.limit {
		return ErrQueueFull
	}
	q.pending = append(q.pending, message)
	return nil
}

func (q *Queue) Drain() []*schema.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	pending := q.pending
	q.pending = nil
	return pending
}

// Close refuses later pushes and returns the messages still queued, so a
// message is either drained or returned here, never silently lost.
func (q *Queue) Close() []*schema.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	pending := q.pending
	q.pending = nil
	return pending
}
