//go:build windows

package local

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func (f *FileSystem) Chmod(ctx context.Context, path string, mode fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("chmod %s: %w", real(path), base.ErrUnsupported)
}
