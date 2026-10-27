package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
)

// prepareDatabaseFile creates a missing database file owner-only and repairs
// the mode of an existing one. It never opens an existing file: closing any
// descriptor on it would release the POSIX locks SQLite holds on that file
// in this process.
func prepareDatabaseFile(path string) error {
	if !ownerOnlyPermissionsSupported() {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	switch {
	case err == nil:
		if err := file.Close(); err != nil {
			return err
		}
	case !errors.Is(err, fs.ErrExist):
		return err
	}
	return os.Chmod(path, 0o600)
}

func hardenDatabaseFiles(path string) error {
	if !ownerOnlyPermissionsSupported() {
		return nil
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(name, 0o600); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("harden %s permissions: %w", name, err)
		}
	}
	return nil
}

func ownerOnlyPermissionsSupported() bool {
	switch runtime.GOOS {
	case "aix", "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "linux", "netbsd", "openbsd", "solaris":
		return true
	default:
		return false
	}
}
