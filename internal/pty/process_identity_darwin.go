//go:build darwin

package pty

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func readUnixProcessIdentity(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", fmt.Errorf("read process start identity for %d: %w", pid, err)
	}
	started := info.Proc.P_starttime
	return fmt.Sprintf("%d:%d", started.Sec, started.Usec), nil
}
