//go:build windows

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func lockHelperState(stateDir string) (func(), error) {
	path := filepath.Join(stateDir, "helper.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open supervisor helper lock: %w", err)
	}
	handle := windows.Handle(file.Fd())
	var overlapped windows.Overlapped
	err = windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, fmt.Errorf("%w: supervisor helper state %s is locked by another helper", ErrAlreadyExists, stateDir)
		}
		return nil, fmt.Errorf("lock supervisor helper state: %w", err)
	}
	return func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
		_ = file.Close()
	}, nil
}
