//go:build !windows

package ssh

import (
	"errors"
	"syscall"
)

func classifySyscallKind(err error) (ErrorKind, bool) {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return ErrorKindRefused, true
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return ErrorKindUnreachable, true
	}
	return "", false
}
