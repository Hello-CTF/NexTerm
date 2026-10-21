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

func startTerminal(cmd *exec.Cmd, cols, rows uint32, processFactory func(*exec.Cmd) (*Process, error)) (terminal, error) {
	master, err := creack.StartWithSize(cmd, &creack.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	master, err = pollableMaster(master)
	if err != nil {
		_ = master.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	if processFactory == nil {
		processFactory = newStartedProcess
	}
	process, err := processFactory(cmd)
	if err != nil {
		_ = master.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	return &unixTerminal{master: master, process: process}, nil
}

// pollableMaster replaces the descriptor after creack's Fd-based ioctls.
// A blocking master can leave Write in the kernel after Close; the duplicate
// lets os.NewFile register nonblocking I/O with the runtime poller.
func pollableMaster(master *os.File) (*os.File, error) {
	connection, err := master.SyscallConn()
	if err != nil {
		return master, err
	}
	duplicateFD := -1
	var duplicateErr error
	if err := connection.Control(func(fd uintptr) {
		duplicateFD, duplicateErr = unix.Dup(int(fd))
	}); err != nil {
		return master, err
	}
	if duplicateErr != nil {
		return master, duplicateErr
	}
	unix.CloseOnExec(duplicateFD)
	if err := unix.SetNonblock(duplicateFD, true); err != nil {
		_ = unix.Close(duplicateFD)
		return master, err
	}
	duplicate := os.NewFile(uintptr(duplicateFD), master.Name())
	if duplicate == nil {
		_ = unix.Close(duplicateFD)
		return master, os.ErrInvalid
	}
	if err := master.Close(); err != nil {
		_ = duplicate.Close()
		return master, err
	}
	return duplicate, nil
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

func (t *unixTerminal) Close() error {
	return t.master.Close()
}
