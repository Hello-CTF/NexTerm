package local

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/pty"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type execChannel struct {
	process    *pty.Process
	stdin      *os.File
	stdout     *os.File
	stderr     *os.File
	ctx        context.Context
	cancel     context.CancelFunc
	id         string
	generation uint64
	stdinOnce  sync.Once
	stdinErr   error
	closeOnce  sync.Once
	closeErr   error
}

func openExecChannel(ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd, id string, generation uint64) (*execChannel, error) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		cancel()
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		cancel()
		closeAll(stdinR, stdinW, stdoutR, stdoutW)
		return nil, err
	}
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	process, err := pty.StartProcess(cmd)
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()
	if err != nil {
		cancel()
		closeAll(stdinW, stdoutR, stderrR)
		return nil, err
	}
	return &execChannel{
		process:    process,
		stdin:      stdinW,
		stdout:     stdoutR,
		stderr:     stderrR,
		ctx:        ctx,
		cancel:     cancel,
		id:         id,
		generation: generation,
	}, nil
}

func (c *execChannel) Read(p []byte) (int, error) {
	return c.stdout.Read(p)
}

func (c *execChannel) Write(p []byte) (int, error) {
	return c.stdin.Write(p)
}

func (c *execChannel) Stderr() io.Reader {
	return c.stderr
}

func (c *execChannel) Resize(ctx context.Context, cols, rows uint32) error {
	return base.ErrUnsupported
}

func (c *execChannel) Wait(ctx context.Context) error {
	waitCtx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(c.ctx, func() { cancel(c.ctx.Err()) })
	defer func() {
		stop()
		cancel(nil)
	}()
	return c.process.Wait(waitCtx)
}

func (c *execChannel) CloseWrite() error {
	c.stdinOnce.Do(func() {
		c.stdinErr = closeFile(c.stdin)
	})
	return c.stdinErr
}

func (c *execChannel) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		killErr := c.process.Kill()
		stdinErr := c.CloseWrite()
		stdoutErr := closeFile(c.stdout)
		stderrErr := closeFile(c.stderr)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.process.Wait(ctx)
		cancel()
		c.closeErr = errors.Join(killErr, stdinErr, stdoutErr, stderrErr)
	})
	return c.closeErr
}

func (c *execChannel) ID() string {
	return c.id
}

func (c *execChannel) Generation() uint64 {
	return c.generation
}

func closeAll(files ...*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

func closeFile(file *os.File) error {
	err := file.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

var _ base.Channel = (*execChannel)(nil)
