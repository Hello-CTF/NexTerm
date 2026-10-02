//go:build windows

package pty

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func prepareCommand(cmd *exec.Cmd) {
	attrs := &syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		copy := *cmd.SysProcAttr
		attrs = &copy
	}
	attrs.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW
	cmd.SysProcAttr = attrs
}

type windowsProcessTree struct {
	process windows.Handle
	job     windows.Handle
}

func newProcessTree(process *os.Process) (processTree, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(process.Pid))
	if err != nil {
		return nil, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		_ = windows.CloseHandle(job)
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return &windowsProcessTree{process: handle, job: job}, nil
}

func (t *windowsProcessTree) Kill() error {
	err := windows.TerminateJobObject(t.job, 1)
	if err == windows.ERROR_INVALID_HANDLE {
		return os.ErrProcessDone
	}
	return err
}

func (t *windowsProcessTree) Close() error {
	err1 := windows.CloseHandle(t.process)
	err2 := windows.CloseHandle(t.job)
	if err1 != nil {
		return err1
	}
	return err2
}

func processSignal(state os.ProcessState) string {
	return ""
}
