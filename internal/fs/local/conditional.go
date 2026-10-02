package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

// The hooks let tests inject adversarial external mutations around the
// final check, including after it and immediately before the commit. They
// are nil in production.
var (
	conditionalPreVerifyHook func()
	conditionalPreCommitHook func()
)

// WriteFileVersion implements conditional.Writer.
//
// A conditional create stages the content beside the target and publishes it
// through a hard link, an atomic primitive that fails instead of overwriting
// if the path appeared at any moment before the commit, so there is no
// check-then-create window.
//
// An existing-file replacement is refused on every platform. No available
// primitive conditions an atomic pathname replacement on the verified
// identity and content against non-cooperating writers: Unix locks are
// advisory, and on Windows the commit itself requires delete sharing, which
// reopens a rename/delete/recreate race after any identity recheck. The
// capability therefore fails with base.ErrUnsupported before any mutation
// instead of claiming a window that cannot be closed.
func (f *FileSystem) WriteFileVersion(ctx context.Context, path string, data []byte, backup bool, expected conditional.Expectation) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path = real(path)
	if expected.Exists {
		return fmt.Errorf("local conditional replace %s: %w: no filesystem primitive conditions an atomic replacement on the verified identity and content against non-cooperating writers", path, base.ErrUnsupported)
	}
	if _, err := os.Lstat(path); err == nil {
		return alreadyExists(expected)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return f.createFileVersion(ctx, path, data, expected)
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
	if conditionalPreVerifyHook != nil {
		conditionalPreVerifyHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return alreadyExists(expected)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if conditionalPreCommitHook != nil {
		conditionalPreCommitHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Publishing through a hard link is atomic and fails instead of
	// overwriting even if the path appeared after the last check.
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

func alreadyExists(expected conditional.Expectation) error {
	return &conditional.MismatchError{Expected: expected, Actual: conditional.Version{Exists: true}, Reason: "file already exists"}
}

var _ conditional.Writer = (*FileSystem)(nil)
