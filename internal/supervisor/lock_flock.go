//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package supervisor

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockSocket(path string) (func(), error) {
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open supervisor socket lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: supervisor socket %s is locked by another server", ErrAlreadyExists, path)
		}
		return nil, fmt.Errorf("lock supervisor socket: %w", err)
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
