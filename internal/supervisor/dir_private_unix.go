//go:build unix

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		return &UntrustedEndpointError{Kind: EndpointStateDir, Path: path, OwnerUID: fileOwnerUID(info)}
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

// fileOwnerUID returns the owner of info, or -1 when unavailable.
func fileOwnerUID(info os.FileInfo) int {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid)
	}
	return -1
}

// checkEndpointDirOwner refuses an existing endpoint directory of another user
// before any probe or spawn. A missing directory is fine.
func checkEndpointDirOwner(endpoint string) error {
	dir := filepath.Dir(endpoint)
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if ownedByExpectedUser(info) != nil {
		return &UntrustedEndpointError{Kind: EndpointSocketDir, Path: dir, OwnerUID: fileOwnerUID(info)}
	}
	return nil
}
