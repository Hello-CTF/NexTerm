//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func lockHelperState(stateDir string) (func(), error) {
	file, err := os.OpenFile(filepath.Join(stateDir, "helper.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open supervisor helper lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: supervisor helper state %s is locked by another helper", ErrAlreadyExists, stateDir)
		}
		return nil, fmt.Errorf("lock supervisor helper state: %w", err)
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
