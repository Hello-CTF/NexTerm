//go:build windows

package supervisor

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/windows"
)

// verifiedHelperPeer returns the process serving the pipe after checking that
// it runs as this user, so a pipe another user created first cannot be
// mistaken for the helper and terminated.
func verifiedHelperPeer(conn net.Conn) (int, error) {
	file, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return 0, fmt.Errorf("%w: helper connection is not a named pipe", ErrProtocol)
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(file.Fd()), &pid); err != nil {
		return 0, fmt.Errorf("helper pipe server process: %w", err)
	}
	if pid == 0 {
		return 0, fmt.Errorf("%w: helper pipe server process is unknown", ErrUnsupported)
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, fmt.Errorf("open helper process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return 0, fmt.Errorf("open helper process token: %w", err)
	}
	defer func() { _ = token.Close() }()
	owner, err := token.GetTokenUser()
	if err != nil {
		return 0, fmt.Errorf("read helper process owner: %w", err)
	}
	self, err := currentUserSIDString()
	if err != nil {
		return 0, err
	}
	if owner.User.Sid.String() != self {
		return 0, fmt.Errorf("%w: helper pipe is served by another user", ErrProtocol)
	}
	return int(pid), nil
}

func processAlive(pid int) bool {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// A pid that no longer exists cannot be opened.
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	event, err := windows.WaitForSingleObject(process, 0)
	return err == nil && event == uint32(windows.WAIT_TIMEOUT)
}

// stopHelperProcess terminates the helper: a Windows helper has no graceful
// stop request, and terminating it closes its pseudoconsoles, which ends the
// processes attached to them.
func stopHelperProcess(pid int) error {
	return killHelperProcess(pid)
}

func killHelperProcess(pid int) error {
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	return windows.TerminateProcess(process, 1)
}
