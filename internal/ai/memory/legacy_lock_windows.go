//go:build windows

package memory

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type legacyMigrationFileLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func newLegacyMigrationFileLock(file *os.File) *legacyMigrationFileLock {
	return &legacyMigrationFileLock{file: file}
}

func (l *legacyMigrationFileLock) tryLock() (bool, error) {
	err := windows.LockFileEx(windows.Handle(l.file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &l.overlapped)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return false, legacyMigrationLockError(err)
}

func (l *legacyMigrationFileLock) close() error {
	return closeLegacyMigrationFile(l.file, func() error {
		return windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	})
}
