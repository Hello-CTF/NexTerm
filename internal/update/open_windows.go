//go:build windows

package update

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// syscallELOOP never matches on Windows, which has no O_NOFOLLOW.
var syscallELOOP = errors.New("no ELOOP on windows")

// openNoFollow opens path for reading. Windows has no O_NOFOLLOW: openRegular's Lstat rejects a link that is
// already there and SameFile rejects a file replaced between the Lstat and the open, but a link created in that
// window by a process of the same user is followed.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}

// openVerifiedCopy opens the private copy of the staged package for reading and shares it for reading only: while
// the handle is open no process can write, delete or rename the file, so the installer that runs it by path runs
// the content that was hashed through this handle. FILE_FLAG_OPEN_REPARSE_POINT opens a link itself rather than
// its target, so a link put in place of the copy fails the hash instead of redirecting it.
func openVerifiedCopy(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}
