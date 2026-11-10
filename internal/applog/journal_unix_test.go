//go:build unix

package applog

import (
	"fmt"
	"os"
	"syscall"
	"testing"
)

func journalStreamOf(t *testing.T, f *os.File) string {
	t.Helper()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d", uint64(stat.Dev), uint64(stat.Ino))
}
