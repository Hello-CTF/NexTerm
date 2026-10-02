//go:build windows

package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
	"golang.org/x/sys/windows"
)

// errTargetNotRegular reports a target that changed into a non-regular file
// between the initial path check and opening it.
var errTargetNotRegular = errors.New("conditional write: target is not a regular file")

// lockTarget opens the target and holds an exclusive lock on it until the
// returned file is closed. The share mode denies other writers and, together
// with the byte-range lock, is enforced by the kernel even for processes
// that do not cooperate with locking, while still permitting the atomic
// replacement at commit time.
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

// replaceFileVersion keeps the share modes and the mandatory byte-range
// lock held from the first verification until after the atomic rename, so a
// non-cooperating process cannot get its content writes in between them.
// The locked file is hashed after staging, the path identity is re-checked
// immediately before the commit, and the backup is taken from the locked,
// verified handle rather than by re-opening the path.
func (f *FileSystem) replaceFileVersion(ctx context.Context, path string, data []byte, backup bool, expected conditional.Expectation) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return notRegular(expected)
		}
	case errors.Is(err, fs.ErrNotExist):
		return missing(expected)
	default:
		return err
	}
	target, err := lockTarget(ctx, path)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return missing(expected)
		case errors.Is(err, errTargetNotRegular):
			return notRegular(expected)
		default:
			return fmt.Errorf("conditional write lock %s: %w", path, err)
		}
	}
	defer target.Close()
	info, err = target.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return notRegular(expected)
	}
	temporary, err := writeTemporary(path, info.Mode(), func(file *os.File) error {
		_, err := file.Write(data)
		return err
	})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporary)
		}
	}()
	if conditionalPreVerifyHook != nil {
		conditionalPreVerifyHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyLockedTarget(ctx, target, path, expected); err != nil {
		return err
	}
	if conditionalPreCommitHook != nil {
		conditionalPreCommitHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := recheckTargetIdentity(target, path, expected); err != nil {
		return err
	}
	if backup {
		if err := backupOpenFile(target, path, info.Mode()); err != nil {
			return fmt.Errorf("backup %s: %w", path, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceFile(temporary, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// verifyLockedTarget confirms that path still names the locked file and
// that the locked file still holds the expected version.
func verifyLockedTarget(ctx context.Context, target *os.File, path string, expected conditional.Expectation) error {
	if err := recheckTargetIdentity(target, path, expected); err != nil {
		return err
	}
	if _, err := target.Seek(0, io.SeekStart); err != nil {
		return err
	}
	actual, err := conditional.HashReader(ctx, target)
	if err != nil {
		return err
	}
	return expected.Check(actual)
}

// recheckTargetIdentity is the final guard against the path having been
// deleted, replaced or turned into a non-regular file before the commit.
func recheckTargetIdentity(target *os.File, path string, expected conditional.Expectation) error {
	current, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return missing(expected)
	}
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() {
		return notRegular(expected)
	}
	locked, err := target.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(locked, current) {
		return &conditional.MismatchError{Expected: expected, Actual: conditional.Version{Exists: true}, Reason: "target was replaced"}
	}
	return nil
}

// backupOpenFile writes the backup from the locked, verified file instead of
// re-opening the path, using the same atomic replacement as backupFile.
func backupOpenFile(source *os.File, path string, mode os.FileMode) error {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	backupPath := path + BackupSuffix
	temporary, err := writeTemporary(backupPath, mode, func(file *os.File) error {
		_, err := io.Copy(file, source)
		return err
	})
	if err != nil {
		return err
	}
	if err := replaceFile(temporary, backupPath); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func missing(expected conditional.Expectation) error {
	return &conditional.MismatchError{Expected: expected, Actual: conditional.Version{}, Reason: "file is missing"}
}

func notRegular(expected conditional.Expectation) error {
	return &conditional.MismatchError{Expected: expected, Actual: conditional.Version{Exists: true}, Reason: "target is not a regular file"}
}
