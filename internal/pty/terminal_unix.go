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
	"golang.org/x/sys/unix"
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
	connection, err := t.master.SyscallConn()
	if err != nil {
		return err
	}
	size := unix.Winsize{Col: uint16(cols), Row: uint16(rows)}
	var resizeErr error
	if err := connection.Control(func(fd uintptr) {
		resizeErr = setWinsize(fd, &size)
	}); err != nil {
		return err
	}
	return resizeErr
}

func (t *unixTerminal) Wait(ctx context.Context) error {
	return t.process.Wait(ctx)
}

func (t *unixTerminal) Kill() error {
	return t.process.Kill()
}

func (t *unixTerminal) CleanupError() error {
	return t.process.CleanupError()
}

func (t *unixTerminal) PID() int {
	return t.process.PID()
}

func (t *unixTerminal) Close() error {
	return t.master.Close()
}
