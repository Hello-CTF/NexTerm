//go:build !windows

package ssh

import (
	"errors"
	"io"
	"os/exec"
	"syscall"
)

func startProxyCommand(command string) (*proxyCommandProcess, io.WriteCloser, io.ReadCloser, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, proxyCommandStartError(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	return &proxyCommandProcess{cmd: cmd}, stdin, stdout, nil
}

type proxyCommandProcess struct {
	cmd *exec.Cmd
}

func (p *proxyCommandProcess) kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (p *proxyCommandProcess) wait() error {
	return p.cmd.Wait()
}

func (p *proxyCommandProcess) close() error {
	return nil
}
