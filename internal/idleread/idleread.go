// Package idleread bounds a download by how long it goes without receiving data instead of by its total duration,
// so a slow but progressing transfer completes while a stalled one fails promptly.
package idleread

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// IdleError is the cancellation cause recorded when no data arrived within the idle window.
type IdleError struct{ Idle time.Duration }

func (e *IdleError) Error() string { return fmt.Sprintf("no data received for %s", e.Idle) }

// Watchdog cancels its context once idle passes without a call to Touch or a read that returns data.
type Watchdog struct {
	idle  time.Duration
	timer *time.Timer
}

// WithIdleTimeout returns a context derived from parent that is canceled with an *IdleError cause when the
// returned Watchdog sees no progress for idle. The watchdog is armed at once, so waiting for response headers
// counts as idle time too. The CancelFunc stops the watchdog and releases the context.
func WithIdleTimeout(parent context.Context, idle time.Duration) (context.Context, *Watchdog, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	w := &Watchdog{idle: idle}
	w.timer = time.AfterFunc(idle, func() { cancel(&IdleError{Idle: idle}) })
	return ctx, w, func() {
		w.timer.Stop()
		cancel(context.Canceled)
	}
}

// Touch records progress and restarts the idle window.
func (w *Watchdog) Touch() { w.timer.Reset(w.idle) }

// Reader wraps r so every read that returns data counts as progress.
func (w *Watchdog) Reader(r io.Reader) io.Reader { return &reader{r: r, w: w} }

type reader struct {
	r io.Reader
	w *Watchdog
}

func (r *reader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.w.Touch()
	}
	return n, err
}

// Cause replaces err with the idle or deadline cause of ctx when ctx ended, so a stalled transfer reports
// "no data received" instead of a bare "context canceled". It returns err unchanged otherwise.
func Cause(ctx context.Context, err error) error {
	if err == nil || ctx.Err() == nil {
		return err
	}
	cause := context.Cause(ctx)
	var idle *IdleError
	if errors.As(cause, &idle) || errors.Is(cause, context.DeadlineExceeded) {
		return fmt.Errorf("%w (%v)", cause, err)
	}
	return err
}
