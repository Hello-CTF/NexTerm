//go:build windows

package ssh

import (
	"errors"
	"os"
	"os/exec"
)

func proxyCommandShell(command string) []string {
	return []string{"cmd", "/C", command}
}

func configureProxyProc(c *exec.Cmd) {}

func killProxyCommand(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	err := c.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
