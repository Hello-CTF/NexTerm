//go:build windows

package local

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// lockTarget opens the target and holds an exclusive lock on it until the
// returned file is closed. The share mode denies other writers and is
// enforced by the kernel even for processes that do not cooperate with
// locking, while still permitting the atomic replacement at commit time.
func lockTarget(ctx context.Context, path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	for {
		handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			file := os.NewFile(uintptr(handle), path)
			lockErr := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 0xFFFFFFFF, 0xFFFFFFFF, new(windows.Overlapped))
			if lockErr == nil {
				return file, nil
			}
			_ = file.Close()
			if !errors.Is(lockErr, windows.ERROR_LOCK_VIOLATION) {
				return nil, &os.PathError{Op: "lock", Path: path, Err: lockErr}
			}
		} else if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
