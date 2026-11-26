package rstream

import (
	"sync"
	"time"
)

// Budget caps the unacknowledged send bytes of every stream sharing it. A writer that finds the budget spent blocks
// like one whose own window is full, until acknowledgements, closed streams or expired retentions return bytes.
// A stream retained without a carrier keeps its share until it resumes or its retention ends.
type Budget struct {
	mu    sync.Mutex
	limit int64
	used  int64
	wake  chan struct{}
}

// NewBudget returns a budget of limit bytes.
func NewBudget(limit int64) *Budget {
	return &Budget{limit: limit, wake: make(chan struct{})}
}

// DefaultBudgetBytes is the budget of a Server and of the client streams that name none.
const DefaultBudgetBytes = 64 << 20

var defaultClientBudget = NewBudget(DefaultBudgetBytes)

// acquire grants up to n bytes, possibly none.
func (b *Budget) acquire(n int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	available := b.limit - b.used
	if available <= 0 {
		return 0
	}
	if int64(n) > available {
		n = int(available)
	}
	b.used += int64(n)
	return n
}

func (b *Budget) release(n int) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= int64(n)
	close(b.wake)
	b.wake = make(chan struct{})
	b.mu.Unlock()
}

func (b *Budget) changed() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wake
}

// Used reports the bytes currently held.
func (b *Budget) Used() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// Clock schedules the resume and retention deadlines; tests inject a manual one.
type Clock interface {
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a scheduled Clock callback.
type Timer interface {
	Stop() bool
}

type realClock struct{}

func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
