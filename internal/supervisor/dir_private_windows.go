//go:build windows

package supervisor

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
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
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidInput, path, err)
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return fmt.Errorf("inspect directory attributes: %w", err)
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%w: %s is a reparse point", ErrInvalidInput, path)
	}
	return nil
}

func privateRegularFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: not a regular file", ErrInvalidInput)
	}
	return nil
}
