//go:build unix

package applog

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// stderrIsJournal reports whether f is the journald stream systemd announced in JOURNAL_STREAM
// ("<device>:<inode>"); a process that inherited the variable but redirected stderr does not match.
func stderrIsJournal(value string, f *os.File) bool {
	device, inode, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok || f == nil {
		return false
	}
	wantDevice, err := strconv.ParseUint(device, 10, 64)
	if err != nil {
		return false
	}
	wantInode, err := strconv.ParseUint(inode, 10, 64)
	if err != nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return uint64(stat.Dev) == wantDevice && uint64(stat.Ino) == wantInode
}
