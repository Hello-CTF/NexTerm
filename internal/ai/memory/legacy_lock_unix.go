//go:build unix

package memory

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type legacyMigrationFileLock struct {
	file *os.File
}

func newLegacyMigrationFileLock(file *os.File) *legacyMigrationFileLock {
	return &legacyMigrationFileLock{file: file}
}

func (l *legacyMigrationFileLock) tryLock() (bool, error) {
	err := unix.Flock(int(l.file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return false, legacyMigrationLockError(err)
}

func (l *legacyMigrationFileLock) close() error {
	return closeLegacyMigrationFile(l.file, func() error {
		return unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	})
}
