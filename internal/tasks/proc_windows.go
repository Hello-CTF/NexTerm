//go:build windows

package tasks

import (
	"errors"
	"os"
	"os/exec"
)

func configureProc(c *exec.Cmd) {}

func killProc(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	err := c.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
