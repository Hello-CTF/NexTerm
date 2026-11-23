//go:build windows

package update

import (
	"errors"
	"os"
)

// syscallELOOP never matches on Windows, which has no O_NOFOLLOW.
var syscallELOOP = errors.New("no ELOOP on windows")

// openNoFollow opens path for reading. Windows has no O_NOFOLLOW: openRegular's Lstat rejects a link that is
// already there and SameFile rejects a file replaced between the Lstat and the open, but a link created in that
// window by a process of the same user is followed.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
