//go:build windows

package shellintegr

import (
	"testing"

	"golang.org/x/sys/windows"
)

// longPath expands 8.3 short components (RUNNER~1) the way the shell reports the directory.
func longPath(t *testing.T, path string) string {
	t.Helper()
	in, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := windows.GetLongPathName(in, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, n)
	n, err = windows.GetLongPathName(in, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	return windows.UTF16ToString(buf[:n])
}
