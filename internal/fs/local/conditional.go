package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var (
	conditionalPreVerifyHook func()
	conditionalPreCommitHook func()
)

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
