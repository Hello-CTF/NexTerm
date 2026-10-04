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

func pipeEndpointName(stateDir string) string {
	sum := sha256.Sum256([]byte(stateDir))
	return fmt.Sprintf(`\\.\pipe\nexterm-supervisor-v%d-%x`, ProtocolVersion, sum[:4])
}

func currentUserPipeSDDL() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return "D:P(A;;GA;;;SY)(A;;GA;;;" + user.User.Sid.String() + ")", nil
}

type pipeAddr struct{ name string }

func (a pipeAddr) Network() string { return "pipe" }

func (a pipeAddr) String() string { return a.name }

type pipeListener struct {
	name      string
	sa        windows.SecurityAttributes
	closed    chan struct{}
	closeOnce sync.Once
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
	listener := &pipeListener{
		name: path,
		sa: windows.SecurityAttributes{
			Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
			SecurityDescriptor: securityDescriptor,
		},
		closed: make(chan struct{}),
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialPipe(probeCtx, path)
	if err == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: supervisor pipe %s is already serving", ErrAlreadyExists, path)
	}
	if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil, fmt.Errorf("%w: supervisor pipe %s is already serving: %v", ErrAlreadyExists, path, err)
	}
	return listener, nil
}

func (l *pipeListener) Accept() (net.Conn, error) {
	name, err := windows.UTF16PtrFromString(l.name)
	if err != nil {
		return nil, err
	}
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	default:
	}
	handle, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, windows.PIPE_UNLIMITED_INSTANCES, pipeBufferSize, pipeBufferSize, 0, &l.sa)
	if err != nil {
		return nil, err
	}
	err = windows.ConnectNamedPipe(handle, nil)
	if err == nil || err == windows.ERROR_PIPE_CONNECTED {
		select {
		case <-l.closed:
			_ = windows.CloseHandle(handle)
			return nil, net.ErrClosed
		default:
		}
		return &pipeConn{handle: handle, name: l.name, server: true}, nil
	}
	_ = windows.CloseHandle(handle)
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	default:
	}
	return nil, err
}

func (l *pipeListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
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
