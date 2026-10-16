//go:build windows

package local

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"golang.org/x/sys/windows"
)

func platformRoots() (base.FileSystemRoots, error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return base.FileSystemRoots{}, fmt.Errorf("enumerate logical drives: %w", err)
	}
	roots := make([]base.FileRoot, 0, 26)
	for drive := uint32(0); drive < 26; drive++ {
		if mask&(1<<drive) == 0 {
			continue
		}
		path := string(rune('A'+drive)) + `:\`
		roots = append(roots, base.FileRoot{Path: path, Label: path[:2]})
	}
	return base.FileSystemRoots{PathStyle: base.PathStyleWindows, Roots: roots, Supported: true}, nil
}

func normalizeFilesystemError(err error) error {
	switch {
	case errors.Is(err, windows.ERROR_NOT_READY),
		errors.Is(err, windows.ERROR_NO_MEDIA_IN_DRIVE),
		errors.Is(err, windows.ERROR_DEVICE_NOT_CONNECTED):
		return fmt.Errorf("%w: %w", base.ErrNoMedia, err)
	case errors.Is(err, windows.ERROR_INVALID_DRIVE), errors.Is(err, windows.ERROR_BAD_UNIT):
		return fmt.Errorf("%w: %w", fs.ErrNotExist, err)
	default:
		return err
	}
}
