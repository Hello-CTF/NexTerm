//go:build windows

package sshconfig

import (
	"os"

	"golang.org/x/sys/windows"
)

// openSharedDelete opens path for parsing with FILE_SHARE_DELETE so a
// concurrent rename or removal (for example the inventory watch deleting the
// config between polls) is never blocked by an in-flight parse, matching the
// Unix unlink-while-open behavior the sync semantics rely on.
func openSharedDelete(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
