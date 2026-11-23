//go:build !windows

package update

import (
	"os"
	"syscall"
)

// syscallELOOP is what opening a symbolic link with O_NOFOLLOW fails with.
var syscallELOOP error = syscall.ELOOP

// openNoFollow opens path for reading and fails on a symbolic link in its last component.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

// openVerifiedCopy opens the private copy of the staged package for the hash that verifies it.
func openVerifiedCopy(path string) (*os.File, error) {
	return openNoFollow(path)
}
