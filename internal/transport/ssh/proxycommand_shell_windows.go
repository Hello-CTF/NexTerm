//go:build windows

package ssh

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

func proxyCommandShell(command string) []string {
	return []string{"cmd", "/C", command}
}

func configureProxyProc(c *exec.Cmd) {}

type proxyCommandProcess struct {
	job windows.Handle
}

func trackProxyCommand(cmd *exec.Cmd) (*proxyCommandProcess, error) {
	if cmd.Process == nil {
		return nil, fmt.Errorf("proxy command process not started")
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(process)
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	return &proxyCommandProcess{job: job}, nil
}

func (p *proxyCommandProcess) kill() error {
	err := windows.TerminateJobObject(p.job, 1)
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		return os.ErrProcessDone
	}
	return err
}

func (p *proxyCommandProcess) close() error {
	return windows.CloseHandle(p.job)
}
