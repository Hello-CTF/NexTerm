//go:build unix && !aix && !solaris

package pty

import "golang.org/x/sys/unix"

func setWinsize(fd uintptr, size *unix.Winsize) error {
	return unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, size)
}
