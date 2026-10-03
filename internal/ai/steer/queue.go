package steer

import (
	"errors"
	"sync"

	"github.com/cloudwego/eino/schema"
)

// Queue is a bounded FIFO of steering messages waiting for the next
// model-call boundary. The production agent runner drains it inside the
// BeforeChatModel middleware, so a steered user message is only ever
// appended between complete tool-call units — never between an assistant
// message with tool calls and their results.
//
// Queue is safe for concurrent use: Steer RPCs push while the agent loop
// drains at boundaries, and cancellation drains whatever is left so the
// caller can report it as undelivered instead of silently losing it.
type Queue struct {
	mu      sync.Mutex
	limit   int
	pending []*schema.Message
}

// NewQueue creates a queue holding at most limit messages. A limit <= 0
// falls back to the Runner default so misconfiguration degrades to the
// bounded behavior rather than an unbounded one.
func NewQueue(limit int) *Queue {
	if limit <= 0 {
		limit = 64
	}
	return &Queue{limit: limit}
}

// Push appends a message to the queue. It returns ErrQueueFull when the
// queue already holds limit messages, and never blocks: a steering
// message that cannot be accepted right away must surface to the caller
// instead of piling up without bound.
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

// Drain removes and returns every queued message in FIFO order. The
// caller takes ownership of the returned slice; the queue is empty
// afterwards regardless of how the caller uses them.
func (q *Queue) Drain() []*schema.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	pending := q.pending
	q.pending = nil
	return pending
}

// Len reports how many messages are currently queued.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}
