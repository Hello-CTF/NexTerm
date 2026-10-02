//go:build !windows

package tasks

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureProc(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProc kills the whole process group so shell children cannot outlive
// the task. A zombie group leader keeps the group ID reserved until Wait,
// so the signal cannot hit a recycled group.
func killProc(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
