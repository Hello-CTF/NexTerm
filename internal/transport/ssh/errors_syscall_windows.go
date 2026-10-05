//go:build windows

package ssh

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

func classifySyscallKind(err error) (ErrorKind, bool) {
	if errors.Is(err, windows.WSAECONNREFUSED) || errors.Is(err, syscall.ECONNREFUSED) {
		return ErrorKindRefused, true
	}
	if errors.Is(err, windows.WSAEHOSTUNREACH) || errors.Is(err, windows.WSAENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return ErrorKindUnreachable, true
	}
	return "", false
}
