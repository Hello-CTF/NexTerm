//go:build linux

package pty

import "golang.org/x/sys/unix"

const ioctlGetTermios = unix.TCGETS
