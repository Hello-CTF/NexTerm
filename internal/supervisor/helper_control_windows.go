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
	denied := func(err error) bool { return errors.Is(err, windows.ERROR_ACCESS_DENIED) }
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(file.Fd()), &pid); err != nil {
		return 0, fmt.Errorf("helper pipe server process: %w", err)
	}
	if pid == 0 {
		return 0, fmt.Errorf("%w: helper pipe server process is unknown", ErrUnsupported)
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		if denied(err) {
			return 0, &UntrustedEndpointError{Kind: EndpointElevated, Elevated: true, OwnerUID: -1}
		}
		return 0, fmt.Errorf("open helper process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		if denied(err) {
			return 0, &UntrustedEndpointError{Kind: EndpointElevated, Elevated: true, OwnerUID: -1}
		}
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
		return 0, &UntrustedEndpointError{Kind: EndpointPipe, OwnerUID: -1}
	}
	return int(pid), nil
}
