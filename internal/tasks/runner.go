package tasks

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

// Process is a running command under a task.
type Process interface {
	// Wait blocks until the process exits and its output has been drained.
	// It returns the exit code; a non-nil error reports a wait failure, not
	// a non-zero exit.
	Wait() (int, error)
	// Kill forcefully terminates the process, including its process group
	// on platforms that support it. Killing an exited process is a no-op.
	Kill() error
}

// Starter launches commands. Integration may substitute remote or sandboxed
// execution; the default is local execution via LocalStarter.
type Starter interface {
	Start(ctx context.Context, cmd Command, output io.Writer) (Process, error)
}

// StarterFunc adapts a function to Starter.
type StarterFunc func(context.Context, Command, io.Writer) (Process, error)

// Start implements Starter.
func (f StarterFunc) Start(ctx context.Context, cmd Command, output io.Writer) (Process, error) {
	return f(ctx, cmd, output)
}

// LocalStarter runs commands as local child processes. The caller's context
// only gates startup: task lifetime is controlled explicitly through Kill so
// a disconnected caller never cancels a detached task implicitly.
type LocalStarter struct {
	// WaitDelay bounds how long Wait keeps draining output pipes after the
	// process exits or is killed. Defaults to two seconds.
	WaitDelay time.Duration
}

// Start implements Starter.
func (s LocalStarter) Start(ctx context.Context, cmd Command, output io.Writer) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := exec.Command(cmd.Path, cmd.Args...)
	c.Dir = cmd.Dir
	c.Env = append(os.Environ(), cmd.Env...)
	c.Stdout = output
	c.Stderr = output
	c.WaitDelay = s.WaitDelay
	if c.WaitDelay <= 0 {
		c.WaitDelay = 2 * time.Second
	}
	configureProc(c)
	if err := c.Start(); err != nil {
		return nil, err
	}
	return &localProcess{cmd: c}, nil
}

type localProcess struct {
	cmd *exec.Cmd
}

func (p *localProcess) Wait() (int, error) {
	err := p.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	if errors.Is(err, exec.ErrWaitDelay) && p.cmd.ProcessState != nil {
		return p.cmd.ProcessState.ExitCode(), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

func (p *localProcess) Kill() error {
	return killProc(p.cmd)
}
