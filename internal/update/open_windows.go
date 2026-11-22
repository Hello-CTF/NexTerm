//go:build windows

package update

import "os"

// openNoFollow opens path for reading; Windows has no O_NOFOLLOW, so openRegular's Lstat and SameFile checks
// reject a link or a file replaced between the check and the open.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
