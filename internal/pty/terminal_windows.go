//go:build windows

package pty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsTerminal struct {
	conpty  *windowsConPty
	process *Process
}

type windowsConPty struct {
	hpc         windows.Handle
	attributes  *windows.ProcThreadAttributeListContainer
	inputRead   windows.Handle
	inputWrite  windows.Handle
	outputRead  windows.Handle
	outputWrite windows.Handle
	slaveClosed bool
	stateMu     sync.Mutex
	closing     bool
	opMu        sync.RWMutex
	closeOnce   sync.Once
	closeErr    error
}

func startTerminal(cmd *exec.Cmd, cols, rows uint32) (terminal, error) {
	conpty, err := newWindowsConPty(cols, rows)
	if err != nil {
		return nil, err
	}
	process, err := startWindowsProcess(cmd, windowsStartOptions{
		attributes:  conpty.attributes,
		afterCreate: conpty.closeSlave,
		conPTY:      true,
	})
	if err != nil {
		_ = conpty.Close()
		return nil, err
	}
	return &windowsTerminal{conpty: conpty, process: process}, nil
}

func newWindowsConPty(cols, rows uint32) (*windowsConPty, error) {
	security := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	c := &windowsConPty{}
	if err := windows.CreatePipe(&c.inputRead, &c.inputWrite, security, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&c.outputRead, &c.outputWrite, security, 0); err != nil {
		_ = c.closeHandles()
		return nil, err
	}
	size := windows.Coord{X: int16(cols), Y: int16(rows)}
	if err := windows.CreatePseudoConsole(size, c.inputRead, c.outputWrite, 0, &c.hpc); err != nil {
		_ = c.closeHandles()
		return nil, err
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(c.hpc)
		_ = c.closeHandles()
		return nil, err
	}
	c.attributes = attributes
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, unsafe.Pointer(c.hpc), unsafe.Sizeof(c.hpc)); err != nil {
		attributes.Delete()
		windows.ClosePseudoConsole(c.hpc)
		_ = c.closeHandles()
		return nil, err
	}
	return c, nil
}

func (t *windowsTerminal) Read(p []byte) (int, error) {
	return t.conpty.Read(p)
}

func (t *windowsTerminal) Write(p []byte) (int, error) {
	return t.conpty.Write(p)
}

func (t *windowsTerminal) Resize(cols, rows uint32) error {
	return t.conpty.Resize(cols, rows)
}

func (t *windowsTerminal) Wait(ctx context.Context) error {
	return t.process.Wait(ctx)
}

func (t *windowsTerminal) Kill() error {
	return t.process.Kill()
}

func (t *windowsTerminal) CleanupError() error {
	return t.process.CleanupError()
}

func (t *windowsTerminal) PID() int {
	return t.process.PID()
}

func (t *windowsTerminal) Close() error {
	return t.conpty.Close()
}

func (c *windowsConPty) Read(p []byte) (int, error) {
	c.opMu.RLock()
	defer c.opMu.RUnlock()
	if c.isClosing() {
		return 0, os.ErrClosed
	}
	var read uint32
	err := windows.ReadFile(c.outputRead, p, &read, nil)
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		return int(read), io.EOF
	}
	return int(read), err
}

func (c *windowsConPty) Write(p []byte) (int, error) {
	c.opMu.RLock()
	defer c.opMu.RUnlock()
	if c.isClosing() {
		return 0, os.ErrClosed
	}
	var written uint32
	err := windows.WriteFile(c.inputWrite, p, &written, nil)
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		return int(written), io.ErrClosedPipe
	}
	return int(written), err
}

func (c *windowsConPty) Resize(cols, rows uint32) error {
	c.opMu.RLock()
	defer c.opMu.RUnlock()
	if c.isClosing() {
		return os.ErrClosed
	}
	return windows.ResizePseudoConsole(c.hpc, windows.Coord{X: int16(cols), Y: int16(rows)})
}

func (c *windowsConPty) closeSlave() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.slaveClosed {
		return nil
	}
	c.slaveClosed = true
	return errors.Join(windows.CloseHandle(c.inputRead), windows.CloseHandle(c.outputWrite))
}

func (c *windowsConPty) isClosing() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.closing
}

func (c *windowsConPty) Close() error {
	c.closeOnce.Do(func() {
		c.stateMu.Lock()
		c.closing = true
		c.stateMu.Unlock()
		_ = windows.CancelIoEx(c.outputRead, nil)
		_ = windows.CancelIoEx(c.inputWrite, nil)
		c.opMu.Lock()
		defer c.opMu.Unlock()
		slaveErr := c.closeSlave()
		masterErr := errors.Join(windows.CloseHandle(c.inputWrite), windows.CloseHandle(c.outputRead))
		c.attributes.Delete()
		closed := make(chan struct{})
		go func() {
			windows.ClosePseudoConsole(c.hpc)
			close(closed)
		}()
		var consoleErr error
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			consoleErr = fmt.Errorf("close ConPTY: %w", context.DeadlineExceeded)
		}
		c.closeErr = errors.Join(slaveErr, masterErr, consoleErr)
	})
	return c.closeErr
}

func (c *windowsConPty) closeHandles() error {
	return errors.Join(
		windows.CloseHandle(c.inputRead),
		windows.CloseHandle(c.inputWrite),
		windows.CloseHandle(c.outputRead),
		windows.CloseHandle(c.outputWrite),
	)
}
