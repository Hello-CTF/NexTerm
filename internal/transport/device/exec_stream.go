package device

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// OpenExec starts a non-interactive command through the supervisor's non-PTY exec and streams its output as it
// is produced (log following, batch progress). The channel has no stdin and no PTY: Write and Resize report
// ErrUnsupported, so interactive consumers must keep using durable terminal sessions.
func (t *Transport) OpenExec(ctx context.Context, command string, options base.ExecOptions) (base.Channel, error) {
	if err := t.checkExec(options); err != nil {
		return nil, err
	}
	var opCtx context.Context
	var cancel context.CancelFunc
	if options.Timeout > 0 {
		opCtx, cancel = context.WithTimeout(ctx, options.Timeout)
	} else {
		opCtx, cancel = context.WithCancel(ctx)
	}
	channel := &execChannel{
		id:         ids.New(),
		generation: t.generation,
		router:     base.NewOutputRouter(opCtx),
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	spec := supervisor.ExecSpec{Command: execCommand(t.platform, command)}
	client := t.client()
	go func() {
		defer close(channel.done)
		defer channel.router.Close()
		stdout, stderr := channel.router.Writer(false), channel.router.Writer(true)
		code, signal, err := client.ExecStream(opCtx, spec, func(isStderr bool, data []byte) error {
			writer := stdout
			if isStderr {
				writer = stderr
			}
			_, writeErr := writer.Write(data)
			return writeErr
		})
		switch {
		case err != nil:
			channel.result = err
		case signal != "" || code != 0:
			channel.result = &base.ExitError{Code: code, Signal: signal}
		}
	}()
	return channel, nil
}

// checkExec rejects an exec for a closed transport, an earlier generation or
// a negative timeout before any bridge is dialled.
func (t *Transport) checkExec(options base.ExecOptions) error {
	if t.closed.Load() {
		return base.ErrClosed
	}
	if options.ExpectedGeneration != 0 && options.ExpectedGeneration != t.generation {
		return fmt.Errorf("%w: device transport generation changed", base.ErrDisconnected)
	}
	if options.Timeout < 0 {
		return fmt.Errorf("exec timeout must not be negative")
	}
	return nil
}

type execChannel struct {
	id         string
	generation uint64
	router     *base.OutputRouter
	cancel     context.CancelFunc
	done       chan struct{}
	result     error
	closed     atomic.Bool
}

func (c *execChannel) Read(p []byte) (int, error) { return c.router.ReadStdout(p) }

func (c *execChannel) Write([]byte) (int, error) {
	return 0, fmt.Errorf("%w: device exec has no stdin", base.ErrUnsupported)
}

func (c *execChannel) Stderr() io.Reader { return c.router.Stderr() }

func (c *execChannel) Resize(context.Context, uint32, uint32) error {
	return fmt.Errorf("%w: device exec has no PTY", base.ErrUnsupported)
}

func (c *execChannel) CloseWrite() error { return nil }

func (c *execChannel) ID() string { return c.id }

func (c *execChannel) Generation() uint64 { return c.generation }

func (c *execChannel) NextOutput(ctx context.Context) (base.OutputEvent, error) {
	return c.router.NextOutput(ctx)
}

func (c *execChannel) Wait(ctx context.Context) error {
	select {
	case <-c.done:
		return c.result
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (c *execChannel) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	c.cancel()
	<-c.done
	return nil
}

var (
	_ base.Channel             = (*execChannel)(nil)
	_ base.OrderedOutput       = (*execChannel)(nil)
	_ base.ExecStreamTransport = (*Transport)(nil)
)
