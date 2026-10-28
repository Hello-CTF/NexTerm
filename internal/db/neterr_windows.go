//go:build windows

package db

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// Winsock reports its own errno values, which syscall's POSIX constants do not match.
func isConnRefused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED) || errors.Is(err, syscall.ECONNREFUSED)
}

func isUnreachable(err error) bool {
	return errors.Is(err, windows.WSAENETUNREACH) || errors.Is(err, windows.WSAEHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH)
}
