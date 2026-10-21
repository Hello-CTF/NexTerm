//go:build windows

package sshconfig

import (
	"os"

	"golang.org/x/sys/windows"
)

// openSharedDelete opens path for reading — config parses and one-shot
// identity-key reads — with FILE_SHARE_DELETE so a concurrent removal or
// rename-away (the inventory watch deleting the config between polls, or the
// first step of an identity-key rotation mid-import) is never blocked by an
// in-flight reader, matching the Unix unlink-while-open behavior the sync
// semantics rely on. Windows still refuses a single-step rename-replace over
// an open file regardless of sharing mode, so rotation writers must move the
// old name aside before renaming the new key in.
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
