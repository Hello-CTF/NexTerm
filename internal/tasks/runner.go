package tasks

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

type Process interface {
	Wait() (int, error)
	Kill() error
}

type Starter interface {
	Start(ctx context.Context, cmd Command, output io.Writer) (Process, error)
}

type StarterFunc func(context.Context, Command, io.Writer) (Process, error)

func (f StarterFunc) Start(ctx context.Context, cmd Command, output io.Writer) (Process, error) {
	return f(ctx, cmd, output)
}

type LocalStarter struct {
	WaitDelay time.Duration
}

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
