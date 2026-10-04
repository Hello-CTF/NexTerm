//go:build !windows

package ssh

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

func proxyCommandShell(command string) []string {
	return []string{"sh", "-c", command}
}

func configureProxyProc(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type proxyCommandProcess struct {
	pid int
}

func trackProxyCommand(cmd *exec.Cmd) (*proxyCommandProcess, error) {
	if cmd.Process == nil {
		return nil, fmt.Errorf("proxy command process not started")
	}
	return &proxyCommandProcess{pid: cmd.Process.Pid}, nil
}

func (p *proxyCommandProcess) kill() error {
	err := syscall.Kill(-p.pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (p *proxyCommandProcess) close() error {
	return nil
}
