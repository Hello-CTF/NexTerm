// Package clock is the time source of the direct-connection runtime, so grant expiry, renewal and the path
// selector's windows can be driven by a fake clock in tests.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Clock tells the time and schedules callbacks.
type Clock interface {
	Now() time.Time
	// AfterFunc runs f on its own goroutine after d.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a scheduled callback.
type Timer interface {
	// Stop cancels the callback; it reports whether it was still pending.
	Stop() bool
}

// Real is the system clock.
type Real struct{}

func (Real) Now() time.Time                            { return time.Now() }
func (Real) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Fake is a manual clock: callbacks run when Advance passes their time, each on its own goroutine like
// time.AfterFunc, and Advance waits for them to return so a test observes their effects.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	seq    int
}

// NewFake returns a fake clock at start.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

type fakeTimer struct {
	clock *Fake
	at    time.Time
	seq   int
	f     func()
	done  bool
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	pending := !t.done
	t.done = true
	return pending
}

func (c *Fake) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Fake) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &fakeTimer{clock: c, at: c.now.Add(d), seq: c.seq, f: f}
	c.timers = append(c.timers, t)
	return t
}

// Pending reports how many callbacks are scheduled.
func (c *Fake) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.done {
			n++
		}
	}
	return n
}

// Advance moves the clock by d, running every callback that falls due in time order; a callback that schedules
// another one inside the window runs too.
func (c *Fake) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		live := c.timers[:0]
		for _, t := range c.timers {
			if !t.done {
				live = append(live, t)
			}
		}
		c.timers = live
		sort.Slice(c.timers, func(i, j int) bool {
			if !c.timers[i].at.Equal(c.timers[j].at) {
				return c.timers[i].at.Before(c.timers[j].at)
			}
			return c.timers[i].seq < c.timers[j].seq
		})
		if len(c.timers) == 0 || c.timers[0].at.After(end) {
			c.now = end
			c.mu.Unlock()
			return
		}
		next := c.timers[0]
		next.done = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			next.f()
		}()
		<-finished
	}
}
