//go:build unix

package pty

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func startProcess(cmd *exec.Cmd) (*Process, error) {
	prepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return newStartedProcess(cmd)
}

func prepareCommand(cmd *exec.Cmd) {
	attrs := &syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		copy := *cmd.SysProcAttr
		attrs = &copy
	}
	attrs.Setpgid = true
	attrs.Pgid = 0
	cmd.SysProcAttr = attrs
}

type unixProcessTree struct {
	process *os.Process
}

func newProcessTree(process *os.Process) (processTree, error) {
	return &unixProcessTree{process: process}, nil
}

func (t *unixProcessTree) Kill() error {
	err := syscall.Kill(-t.process.Pid, syscall.SIGKILL)
	if err == nil {
		return nil
	}
	err = t.process.Kill()
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (t *unixProcessTree) Close() error {
	return nil
}

func processSignal(state os.ProcessState) string {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return status.Signal().String()
}
