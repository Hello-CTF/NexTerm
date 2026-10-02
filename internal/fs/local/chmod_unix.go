//go:build unix

package local

import (
	"context"
	"io/fs"
	"os"
)

func (f *FileSystem) Chmod(ctx context.Context, path string, mode fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Chmod(real(path), mode)
}
