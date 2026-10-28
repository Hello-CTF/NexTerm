//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package pty

import "golang.org/x/sys/unix"

const ioctlGetTermios = unix.TIOCGETA
