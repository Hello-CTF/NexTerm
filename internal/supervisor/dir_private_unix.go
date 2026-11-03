//go:build unix

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func ensurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is not a real directory", ErrInvalidInput, path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: %s must not be accessible by group or other users", ErrInvalidInput, path)
	}
	if err := ownedByExpectedUser(info); err != nil {
		return fmt.Errorf("%w: %s is owned by another user", ErrInvalidInput, path)
	}
	return nil
}

// ownedByExpectedUser fails closed when the owner cannot be read.
func ownedByExpectedUser(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("file owner is unavailable")
	}
	if int(stat.Uid) != expectedPeerUID() {
		return fmt.Errorf("owner uid %d is not %d", stat.Uid, expectedPeerUID())
	}
	return nil
}

func privateRegularFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: not a private regular file", ErrInvalidInput)
	}
	return nil
}
