//go:build windows

package supervisor

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const pipeBufferSize = 64 * 1024

func pipeEndpointName(stateDir string) (string, error) {
	identity, err := currentUserIdentity()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(identity + "\x00" + stateDir))
	return fmt.Sprintf(`\\.\pipe\nexterm-supervisor-v%d-%s-%x`, ProtocolVersion, identity, sum[:8]), nil
}

func currentUserSIDString() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func currentUserPipeSDDL() (string, error) {
	sid, err := currentUserSIDString()
	if err != nil {
		return "", err
	}
	return "D:P(A;;GA;;;SY)(A;;GA;;;" + sid + ")", nil
}

type pipeAddr struct{ name string }

func (a pipeAddr) Network() string { return "pipe" }

func (a pipeAddr) String() string { return a.name }

type pipeListener struct {
	name       string
	sa         windows.SecurityAttributes
	mu         sync.Mutex
	pending    windows.Handle
	hasPending bool
	closed     chan struct{}
	closeOnce  sync.Once
}

func listenPipe(path string) (net.Listener, error) {
	sddl, err := currentUserPipeSDDL()
	if err != nil {
		return nil, fmt.Errorf("supervisor pipe security: %w", err)
	}
	securityDescriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("supervisor pipe security descriptor: %w", err)
	}
	sa := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: securityDescriptor,
	}
	listener := &pipeListener{name: path, sa: sa, closed: make(chan struct{})}
	handle, err := listener.createInstance(true)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, fmt.Errorf("%w: supervisor pipe %s is already serving", ErrAlreadyExists, path)
		}
		return nil, err
	}
	listener.pending = handle
	listener.hasPending = true
	return listener, nil
}

func (l *pipeListener) createInstance(first bool) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(l.name)
	if err != nil {
		return 0, err
	}
	flags := uint32(windows.PIPE_ACCESS_DUPLEX)
	if first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	handle, err := windows.CreateNamedPipe(name, flags, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, windows.PIPE_UNLIMITED_INSTANCES, pipeBufferSize, pipeBufferSize, 0, &l.sa)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return 0, fmt.Errorf("%w: pipe name %s is invalid", ErrInvalidInput, l.name)
		}
		return 0, err
	}
	return handle, nil
}

func (l *pipeListener) isClosed() bool {
	select {
	case <-l.closed:
		return true
	default:
		return false
	}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.isClosed() {
		l.mu.Unlock()
		return nil, net.ErrClosed
	}
	handle := l.pending
	hasPending := l.hasPending
	l.pending = 0
	l.hasPending = false
	l.mu.Unlock()
	if !hasPending {
		var err error
		handle, err = l.createInstance(false)
		if err != nil {
			return nil, err
		}
	}
	if l.isClosed() {
		_ = windows.CloseHandle(handle)
		return nil, net.ErrClosed
	}
	err := windows.ConnectNamedPipe(handle, nil)
	if err == nil || err == windows.ERROR_PIPE_CONNECTED {
		if l.isClosed() {
			_ = windows.CloseHandle(handle)
			return nil, net.ErrClosed
		}
		l.mu.Lock()
		if !l.isClosed() {
			if next, err := l.createInstance(false); err == nil {
				l.pending = next
				l.hasPending = true
			}
		}
		l.mu.Unlock()
		return &pipeConn{handle: handle, name: l.name, server: true}, nil
	}
	_ = windows.CloseHandle(handle)
	if l.isClosed() {
		return nil, net.ErrClosed
	}
	return nil, err
}

func (l *pipeListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		l.mu.Lock()
		pending := l.pending
		l.pending = 0
		l.hasPending = false
		l.mu.Unlock()
		if pending != 0 {
			_ = windows.CloseHandle(pending)
		}
		name, err := windows.UTF16PtrFromString(l.name)
		if err != nil {
			return
		}
		for attempt := 0; attempt < 200; attempt++ {
			handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
			if err == nil {
				_ = windows.CloseHandle(handle)
				return
			}
			if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	})
	return nil
}

func (l *pipeListener) Addr() net.Addr {
	return pipeAddr{name: l.name}
}

type pipeConn struct {
	handle    windows.Handle
	name      string
	server    bool
	closeOnce sync.Once
}

func (c *pipeConn) Read(p []byte) (int, error) {
	var done uint32
	err := windows.ReadFile(c.handle, p, &done, nil)
	if err != nil {
		if err == windows.ERROR_BROKEN_PIPE {
			return 0, io.EOF
		}
		return int(done), err
	}
	return int(done), nil
}

func (c *pipeConn) Write(p []byte) (int, error) {
	var done uint32
	err := windows.WriteFile(c.handle, p, &done, nil)
	return int(done), err
}

func (c *pipeConn) Close() error {
	c.closeOnce.Do(func() {
		if c.server {
			_ = windows.DisconnectNamedPipe(c.handle)
		}
		_ = windows.CloseHandle(c.handle)
	})
	return nil
}

func (c *pipeConn) LocalAddr() net.Addr  { return pipeAddr{name: c.name} }
func (c *pipeConn) RemoteAddr() net.Addr { return pipeAddr{name: c.name} }

func (c *pipeConn) SetDeadline(time.Time) error      { return ErrUnsupported }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return ErrUnsupported }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return ErrUnsupported }

func dialPipe(ctx context.Context, path string) (net.Conn, error) {
	if !strings.HasPrefix(path, pipePrefix) {
		return nil, fmt.Errorf("%w: local IPC transport on %s", ErrUnsupported, path)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	for {
		handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			return &pipeConn{handle: handle, name: path}, nil
		}
		if err != windows.ERROR_PIPE_BUSY {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-time.After(25 * time.Millisecond):
		}
	}
}
