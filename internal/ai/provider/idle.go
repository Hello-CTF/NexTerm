package provider

import (
	"fmt"
	"io"
	"time"
)

type idleTimeoutError struct {
	idle time.Duration
}

func (e *idleTimeoutError) Error() string {
	return fmt.Sprintf("AI response stalled: no data for %s", e.idle)
}

func (e *idleTimeoutError) Timeout() bool { return true }

func (e *idleTimeoutError) Temporary() bool { return true }

type idleTimeoutBody struct {
	body io.ReadCloser
	idle time.Duration
}

func newIdleTimeoutBody(body io.ReadCloser, idle time.Duration) io.ReadCloser {
	if idle <= 0 {
		return body
	}
	return &idleTimeoutBody{body: body, idle: idle}
}

func (b *idleTimeoutBody) Read(target []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := b.body.Read(target)
		done <- result{n: n, err: err}
	}()
	timer := time.NewTimer(b.idle)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.n, r.err
	case <-timer.C:
		_ = b.body.Close()
		<-done
		return 0, &idleTimeoutError{idle: b.idle}
	}
}

func (b *idleTimeoutBody) Close() error {
	return b.body.Close()
}
