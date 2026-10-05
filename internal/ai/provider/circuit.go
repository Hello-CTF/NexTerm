package provider

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	DefaultCircuitThreshold = 5
	DefaultCircuitCooldown  = 5 * time.Minute
)

var ErrCircuitOpen = errors.New("AI provider circuit open")

type CircuitSnapshot struct {
	ConsecutiveFailures int
	OpenUntil           time.Time
}

func (s CircuitSnapshot) Open(now time.Time) bool {
	return now.Before(s.OpenUntil)
}

type CircuitBreaker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	mu        sync.Mutex
	failures  int
	openUntil time.Time
}

func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = DefaultCircuitThreshold
	}
	if cooldown <= 0 {
		cooldown = DefaultCircuitCooldown
	}
	return &CircuitBreaker{threshold: threshold, cooldown: cooldown, now: time.Now}
}

func (b *CircuitBreaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.now().Before(b.openUntil)
}

func (b *CircuitBreaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openUntil = time.Time{}
}

func (b *CircuitBreaker) RecordFailure(class ErrorClass) {
	if class != ErrorClassTransport && class != ErrorClassServer {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.failures >= b.threshold {
		b.openUntil = b.now().Add(b.cooldown)
	}
}

func (b *CircuitBreaker) Snapshot() CircuitSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return CircuitSnapshot{ConsecutiveFailures: b.failures, OpenUntil: b.openUntil}
}

func (c *Client) circuitOpenError() error {
	snapshot := c.circuit.Snapshot()
	return fmt.Errorf("%w: cooldown until %s", ErrCircuitOpen, snapshot.OpenUntil.Format(time.RFC3339))
}

func (c *Client) recordCircuitOutcome(err error) {
	if c.circuit == nil {
		return
	}
	if err == nil {
		c.circuit.RecordSuccess()
		return
	}
	c.circuit.RecordFailure(ClassifyError(err))
}
