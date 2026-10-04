//go:build !windows

package ssh

import (
	"errors"
	"os/exec"
	"syscall"
)

func proxyCommandShell(command string) []string {
	return []string{"sh", "-c", command}
}

func configureProxyProc(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProxyCommand(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
