package pty

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type processTree interface {
	Kill() error
	Close() error
}

type Process struct {
	tree       processTree
	done       chan struct{}
	waitErr    error
	cleanupErr error
	mu         sync.Mutex
	terminated bool
}

func StartProcess(cmd *exec.Cmd) (*Process, error) {
	return startProcess(cmd)
}

func newProcess(tree processTree, wait func() error) *Process {
	p := &Process{tree: tree, done: make(chan struct{})}
	go p.wait(wait)
	return p
}

func (p *Process) wait(wait func() error) {
	err := wait()
	p.mu.Lock()
	p.waitErr = exitError(err)
	killErr := p.tree.Kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	p.cleanupErr = errors.Join(killErr, p.tree.Close())
	p.terminated = true
	close(p.done)
	p.mu.Unlock()
}

func (p *Process) Wait(ctx context.Context) error {
	select {
	case <-p.done:
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return p.waitErr
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (p *Process) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminated {
		return nil
	}
	err := p.tree.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func (p *Process) CleanupError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cleanupErr
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
