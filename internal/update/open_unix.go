//go:build !windows

package update

import (
	"os"
	"syscall"
)

// openNoFollow opens path for reading and fails on a symbolic link in its last component.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
