package steer

import (
	"errors"
	"sync"

	"github.com/cloudwego/eino/schema"
)

var ErrQueueFull = errors.New("steering queue is full")

type Queue struct {
	mu      sync.Mutex
	limit   int
	pending []*schema.Message
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
