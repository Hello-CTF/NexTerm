package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	gossh "golang.org/x/crypto/ssh"
)

var nextChannelID atomic.Uint64

type channel struct {
	session    *gossh.Session
	stdin      io.WriteCloser
	stdout     io.Reader
	stderr     io.Reader
	output     *base.OutputRouter
	ctx        context.Context
	cancel     context.CancelFunc
	stop       func() bool
	generation uint64
	id         string
	resizable  bool
	waitOnce   sync.Once
	waitDone   chan struct{}
	waitErr    error
	closeOnce  sync.Once
	closeErr   error
}

type sessionResult struct {
	session *gossh.Session
	err     error
}

func (c *Client) newSession(ctx context.Context) (*gossh.Session, error) {
	result := make(chan sessionResult, 1)
	go func() {
		session, err := c.ssh.NewSession()
		result <- sessionResult{session: session, err: err}
	}()
	cleanup := func() {
		go func() {
			late := <-result
			if late.session != nil {
				late.session.Close()
			}
		}()
	}
	select {
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	case <-c.done:
		cleanup()
		return nil, base.ErrDisconnected
	case result := <-result:
		if result.err != nil {
			return nil, result.err
		}
		return result.session, nil
	}
}

func (c *Client) OpenExec(ctx context.Context, command string, options base.ExecOptions) (base.Channel, error) {
	if err := c.check(ctx, options.ExpectedGeneration); err != nil {
		return nil, err
	}
	opCtx := ctx
	cancel := func() {}
	if options.Timeout > 0 {
		opCtx, cancel = context.WithTimeout(ctx, options.Timeout)
	} else {
		opCtx, cancel = context.WithCancel(ctx)
	}
	session, err := c.newSession(opCtx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open SSH session: %w", err)
	}
	stop := context.AfterFunc(opCtx, func() { session.Close() })
	failed := true
	defer func() {
		if failed {
			stop()
			cancel()
			session.Close()
		}
	}()
	stdin, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	output := base.NewOutputRouter(opCtx)
	session.Stdout = output.Writer(false)
	session.Stderr = output.Writer(true)
	if err := session.Start(command); err != nil {
		if opCtx.Err() != nil {
			return nil, opCtx.Err()
		}
		return nil, fmt.Errorf("start SSH exec: %w", err)
	}
	failed = false
	channel := newChannel(session, stdin, nil, nil, output, opCtx, cancel, stop, c.generation, false)
	channel.startWait()
	return channel, nil
}

func (c *Client) OpenPTY(ctx context.Context, options base.PTYOptions) (base.Channel, error) {
	if err := c.check(ctx, options.ExpectedGeneration); err != nil {
		return nil, err
	}
	if options.Cols == 0 {
		options.Cols = 80
	}
	if options.Rows == 0 {
		options.Rows = 24
	}
	if options.Term == "" {
		options.Term = "xterm-256color"
	}
	opCtx, cancel := context.WithCancel(ctx)
	session, err := c.newSession(opCtx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open SSH session: %w", err)
	}
	stop := context.AfterFunc(opCtx, func() { session.Close() })
	failed := true
	defer func() {
		if failed {
			stop()
			cancel()
			session.Close()
		}
	}()
	stdin, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return nil, err
	}
	modes := gossh.TerminalModes{}
	for key, value := range options.Modes {
		modes[key] = value
	}
	if err := session.RequestPty(options.Term, int(options.Rows), int(options.Cols), modes); err != nil {
		if opCtx.Err() != nil {
			return nil, opCtx.Err()
		}
		return nil, fmt.Errorf("request SSH PTY: %w", err)
	}
	if err := session.Shell(); err != nil {
		if opCtx.Err() != nil {
			return nil, opCtx.Err()
		}
		return nil, fmt.Errorf("start SSH shell: %w", err)
	}
	failed = false
	return newChannel(session, stdin, stdout, stderr, nil, opCtx, cancel, stop, c.generation, true), nil
}

func newChannel(session *gossh.Session, stdin io.WriteCloser, stdout, stderr io.Reader, output *base.OutputRouter, ctx context.Context, cancel context.CancelFunc, stop func() bool, generation uint64, resizable bool) *channel {
	return &channel{
		session:    session,
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
		output:     output,
		ctx:        ctx,
		cancel:     cancel,
		stop:       stop,
		generation: generation,
		id:         fmt.Sprintf("ssh-%d", nextChannelID.Add(1)),
		resizable:  resizable,
		waitDone:   make(chan struct{}),
	}
}

func (c *channel) Read(p []byte) (int, error) {
	if c.output != nil {
		return c.output.ReadStdout(p)
	}
	return c.stdout.Read(p)
}

func (c *channel) Write(p []byte) (int, error) {
	return c.stdin.Write(p)
}

func (c *channel) Stderr() io.Reader {
	if c.output != nil {
		return c.output.Stderr()
	}
	return c.stderr
}

func (c *channel) NextOutput(ctx context.Context) (base.OutputEvent, error) {
	if c.output == nil {
		return base.OutputEvent{}, base.ErrUnsupported
	}
	return c.output.NextOutput(ctx)
}

func (c *channel) Resize(ctx context.Context, cols, rows uint32) error {
	if !c.resizable {
		return base.ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if cols == 0 || rows == 0 {
		return fmt.Errorf("PTY dimensions must be positive")
	}
	return c.session.WindowChange(int(rows), int(cols))
}

func (c *channel) startWait() {
	c.waitOnce.Do(func() {
		go func() {
			c.waitErr = mapWaitError(c.session.Wait())
			if c.output != nil {
				c.output.Close()
			}
			close(c.waitDone)
		}()
	})
}

func (c *channel) Wait(ctx context.Context) error {
	c.startWait()
	select {
	case <-c.waitDone:
		return c.waitErr
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

func (c *channel) CloseWrite() error {
	return c.stdin.Close()
}

func (c *channel) Close() error {
	c.closeOnce.Do(func() {
		c.stop()
		c.cancel()
		if c.output != nil {
			c.output.Close()
		}
		c.closeErr = c.session.Close()
	})
	return c.closeErr
}

func (c *channel) ID() string {
	return c.id
}

func (c *channel) Generation() uint64 {
	return c.generation
}

func mapWaitError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *gossh.ExitError
	if errors.As(err, &exitErr) {
		return &base.ExitError{Code: exitErr.ExitStatus(), Signal: exitErr.Signal()}
	}
	var missing *gossh.ExitMissingError
	if errors.As(err, &missing) {
		return fmt.Errorf("%w: %v", base.ErrExitStatusMissing, err)
	}
	return err
}
