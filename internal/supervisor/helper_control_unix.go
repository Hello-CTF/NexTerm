//go:build unix

package supervisor

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// verifiedHelperPeer returns the process serving conn after checking that it
// runs as this user, so a socket another user planted cannot be signalled.
func verifiedHelperPeer(conn net.Conn) (int, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("%w: helper connection is not a unix socket", ErrProtocol)
	}
	uid, err := peerUID(unixConn)
	if err != nil {
		return 0, err
	}
	if uid != os.Geteuid() {
		return 0, fmt.Errorf("%w: helper peer uid %d is not %d", ErrProtocol, uid, os.Geteuid())
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var opErr error
	if err := raw.Control(func(fd uintptr) { pid, opErr = peerPIDFD(fd) }); err != nil {
		return 0, err
	}
	if opErr != nil {
		return 0, opErr
	}
	if pid <= 0 {
		return 0, fmt.Errorf("%w: helper peer pid is unknown", ErrUnsupported)
	}
	return pid, nil
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func stopHelperProcess(pid int) error {
	return ignoreGone(syscall.Kill(pid, syscall.SIGTERM))
}

func killHelperProcess(pid int) error {
	return ignoreGone(syscall.Kill(pid, syscall.SIGKILL))
}

func ignoreGone(err error) error {
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
