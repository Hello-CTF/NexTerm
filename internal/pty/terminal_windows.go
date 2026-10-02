//go:build windows

package pty

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/UserExistsError/conpty"
)

type windowsTerminal struct {
	conpty  *conpty.ConPty
	process processTree
	pid     int
	done    chan struct{}
	waitErr error
	mu      sync.Mutex
	doneful bool
}

func startTerminal(cmd *exec.Cmd, cols, rows uint32) (terminal, error) {
	options := []conpty.ConPtyOption{conpty.ConPtyDimensions(int(cols), int(rows))}
	if cmd.Env != nil {
		options = append(options, conpty.ConPtyEnv(cmd.Env))
	}
	if cmd.Dir != "" {
		options = append(options, conpty.ConPtyWorkDir(cmd.Dir))
	}
	terminal, err := conpty.Start(windowsCommandLine(cmd), options...)
	if err != nil {
		return nil, err
	}
	process, err := newProcessTree(&os.Process{Pid: terminal.Pid()})
	if err != nil {
		_ = terminal.Close()
		return nil, err
	}
	t := &windowsTerminal{conpty: terminal, process: process, pid: terminal.Pid(), done: make(chan struct{})}
	go t.wait()
	return t, nil
}

func windowsCommandLine(cmd *exec.Cmd) string {
	parts := make([]string, len(cmd.Args))
	parts[0] = syscall.EscapeArg(cmd.Path)
	for i := 1; i < len(cmd.Args); i++ {
		parts[i] = syscall.EscapeArg(cmd.Args[i])
	}
	return strings.Join(parts, " ")
}

func (t *windowsTerminal) wait() {
	code, err := t.conpty.Wait(context.Background())
	t.mu.Lock()
	if err != nil {
		t.waitErr = err
	} else if code != 0 {
		t.waitErr = &base.ExitError{Code: int(int32(code))}
	}
	t.doneful = true
	_ = t.process.Close()
	close(t.done)
	t.mu.Unlock()
}

func (t *windowsTerminal) Read(p []byte) (int, error) {
	return t.conpty.Read(p)
}

func (t *windowsTerminal) Write(p []byte) (int, error) {
	return t.conpty.Write(p)
}

func (t *windowsTerminal) Resize(cols, rows uint32) error {
	return t.conpty.Resize(int(cols), int(rows))
}

func (t *windowsTerminal) Wait(ctx context.Context) error {
	select {
	case <-t.done:
		return t.waitErr
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (t *windowsTerminal) Kill() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.doneful {
		return nil
	}
	return t.process.Kill()
}

func (t *windowsTerminal) PID() int {
	return t.pid
}

func (t *windowsTerminal) Close() error {
	return t.conpty.Close()
}
