//go:build aix || solaris

package pty

import "golang.org/x/sys/unix"

func setWinsize(fd uintptr, size *unix.Winsize) error {
	request := uint(unix.TIOCSWINSZ)
	return unix.IoctlSetWinsize(int(fd), int(int32(uint32(request))), size)
}
