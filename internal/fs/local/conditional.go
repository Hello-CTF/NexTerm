package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
)

// errTargetNotRegular reports a target that changed into a non-regular file
// between the initial path check and opening it.
var errTargetNotRegular = errors.New("conditional write: target is not a regular file")

// conditionalTestHook lets tests inject an external mutation after the
// replacement content is staged and before the final verification.
var conditionalTestHook func()

// WriteFileVersion implements conditional.Writer. The target is opened
// without following symlinks and locked for the whole operation; the locked
// file itself is re-hashed after the replacement content has been staged, so
// any external write made before the final verification rejects the commit
// instead of being truncated. The atomic temporary-file replacement and
// backup format of WriteFile are preserved. An existing target must be
// openable read-write so locking and verification share one handle.
func (f *FileSystem) WriteFileVersion(ctx context.Context, path string, data []byte, backup bool, expected conditional.Expectation) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path = real(path)
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !expected.Exists {
			return alreadyExists(expected)
		}
		if !info.Mode().IsRegular() {
			return notRegular(expected)
		}
	case errors.Is(err, fs.ErrNotExist):
		if expected.Exists {
			return missing(expected)
		}
	default:
		return err
	}
	if expected.Exists {
		return f.replaceFileVersion(ctx, path, data, backup, expected)
	}
	return f.createFileVersion(ctx, path, data, expected)
}

func (f *FileSystem) replaceFileVersion(ctx context.Context, path string, data []byte, backup bool, expected conditional.Expectation) error {
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
	info, err := target.Stat()
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
	if conditionalTestHook != nil {
		conditionalTestHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyLockedTarget(ctx, target, path, expected); err != nil {
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

// verifyLockedTarget confirms that path still names the locked file and that
// the locked file still holds the expected version.
func verifyLockedTarget(ctx context.Context, target *os.File, path string, expected conditional.Expectation) error {
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
	if _, err := target.Seek(0, io.SeekStart); err != nil {
		return err
	}
	actual, err := conditional.HashReader(ctx, target)
	if err != nil {
		return err
	}
	return expected.Check(actual)
}

func (f *FileSystem) createFileVersion(ctx context.Context, path string, data []byte, expected conditional.Expectation) error {
	temporary, err := writeTemporary(path, 0o600, func(file *os.File) error {
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
	if conditionalTestHook != nil {
		conditionalTestHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return alreadyExists(expected)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Publishing through a hard link is atomic and fails instead of
	// overwriting if the path appeared while the content was staged.
	if err := os.Link(temporary, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return alreadyExists(expected)
		}
		return fmt.Errorf("conditional create %s: %w", path, err)
	}
	committed = true
	_ = os.Remove(temporary)
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

func alreadyExists(expected conditional.Expectation) error {
	return &conditional.MismatchError{Expected: expected, Actual: conditional.Version{Exists: true}, Reason: "file already exists"}
}

func notRegular(expected conditional.Expectation) error {
	return &conditional.MismatchError{Expected: expected, Actual: conditional.Version{Exists: true}, Reason: "target is not a regular file"}
}

var _ conditional.Writer = (*FileSystem)(nil)
