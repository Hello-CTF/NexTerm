package pty

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type processTree interface {
	Kill() error
	Close() error
}

type Process struct {
	pid     int
	tree    processTree
	done    chan struct{}
	waitErr error
	mu      sync.Mutex
	doneful bool
}

func StartProcess(cmd *exec.Cmd) (*Process, error) {
	prepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return newStartedProcess(cmd)
}

func newStartedProcess(cmd *exec.Cmd) (*Process, error) {
	tree, err := newProcessTree(cmd.Process)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	p := &Process{pid: cmd.Process.Pid, tree: tree, done: make(chan struct{})}
	go p.wait(cmd)
	return p, nil
}

func (p *Process) wait(cmd *exec.Cmd) {
	err := cmd.Wait()
	p.mu.Lock()
	p.waitErr = exitError(err)
	p.doneful = true
	_ = p.tree.Close()
	close(p.done)
	p.mu.Unlock()
}

func (p *Process) PID() int {
	return p.pid
}

func (p *Process) Wait(ctx context.Context) error {
	select {
	case <-p.done:
		return p.waitErr
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (p *Process) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.doneful {
		return nil
	}
	err := p.tree.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func exitError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &base.ExitError{Code: exitErr.ExitCode(), Signal: processSignal(*exitErr.ProcessState)}
	}
	return err
}
