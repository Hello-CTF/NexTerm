//go:build windows

package local

import (
	"context"
	"io/fs"
	"os"
)

// Chmod applies the subset of the file mode Windows supports: the owner
// write bit (0o200) maps to FILE_ATTRIBUTE_READONLY — clear means writable,
// set means read-only. The remaining permission bits have no Windows
// representation and are ignored by the operating system.
func (f *FileSystem) Chmod(ctx context.Context, path string, mode fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Chmod(real(path), mode)
}
