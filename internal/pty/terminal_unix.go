//go:build unix

package pty

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"

	creack "github.com/creack/pty"
)

type unixTerminal struct {
	master  *os.File
	process *Process
}

func startTerminal(cmd *exec.Cmd, cols, rows uint32) (terminal, error) {
	master, err := creack.StartWithSize(cmd, &creack.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	process, err := newStartedProcess(cmd)
	if err != nil {
		_ = master.Close()
		return nil, err
	}
	return &unixTerminal{master: master, process: process}, nil
}

func (t *unixTerminal) Read(p []byte) (int, error) {
	n, err := t.master.Read(p)
	if errors.Is(err, syscall.EIO) {
		return n, io.EOF
	}
	return n, err
}

func (t *unixTerminal) Write(p []byte) (int, error) {
	return t.master.Write(p)
}

func (t *unixTerminal) Resize(cols, rows uint32) error {
	return creack.Setsize(t.master, &creack.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (t *unixTerminal) Wait(ctx context.Context) error {
	return t.process.Wait(ctx)
}

func (t *unixTerminal) Kill() error {
	return t.process.Kill()
}

func (t *unixTerminal) PID() int {
	return t.process.PID()
}

func (t *unixTerminal) Close() error {
	return t.master.Close()
}
