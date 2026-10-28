//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package pty

import "golang.org/x/sys/unix"

// foreground queries the master side. On these systems the terminal ioctls
// on a pseudo terminal master report the slave's foreground process group
// and termios, which is the line discipline the program reads through.
func (t *unixTerminal) foreground() (Foreground, error) {
	connection, err := t.master.SyscallConn()
	if err != nil {
		return Foreground{}, err
	}
	result := Foreground{LeaderPID: t.pid}
	var pgrpErr error
	if err := connection.Control(func(fd uintptr) {
		result.ProcessGroup, pgrpErr = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
		if termios, termErr := unix.IoctlGetTermios(int(fd), ioctlGetTermios); termErr == nil {
			result.Canonical = termios.Lflag&unix.ICANON != 0
			result.CanonicalKnown = true
		}
	}); err != nil {
		return Foreground{}, err
	}
	if pgrpErr != nil {
		return Foreground{}, pgrpErr
	}
	return result, nil
}
