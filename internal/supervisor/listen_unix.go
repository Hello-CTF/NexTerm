//go:build unix

package supervisor

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

func listenSocket(path string) (net.Listener, error) {
	if _, err := os.Lstat(path); err == nil {
		conn, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("%w: supervisor socket %s is already serving", ErrAlreadyExists, path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale supervisor socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect supervisor socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on supervisor socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("secure supervisor socket: %w", err)
	}
	return listener, nil
}

func peerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid int
	var opErr error
	if err := raw.Control(func(fd uintptr) { uid, opErr = peerUIDFD(fd) }); err != nil {
		return 0, err
	}
	return uid, opErr
}
