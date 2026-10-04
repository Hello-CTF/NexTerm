//go:build windows

package ssh

import (
	"errors"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func proxyCommandShell(command string) []string {
	return []string{"cmd", "/C", command}
}

type proxyCommandProcess struct {
	process windows.Handle
	job     windows.Handle
}

func startProxyCommand(shell []string) (*proxyCommandProcess, io.WriteCloser, io.ReadCloser, error) {
	application, err := windows.UTF16PtrFromString(shell[0])
	if err != nil {
		return nil, nil, nil, proxyCommandStartError(err)
	}
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(shell))
	if err != nil {
		return nil, nil, nil, proxyCommandStartError(err)
	}
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, proxyCommandStartError(err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	childHandles := []*os.File{stdinRead, stdoutWrite, devNull}
	cleanupPipes := func() {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		_ = devNull.Close()
	}
	for _, file := range childHandles {
		handle := windows.Handle(file.Fd())
		if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			cleanupPipes()
			return nil, nil, nil, proxyCommandStartError(err)
		}
	}
	startup := new(windows.StartupInfoEx)
	startup.Cb = uint32(unsafe.Sizeof(startup.StartupInfo))
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput = windows.Handle(stdinRead.Fd())
	startup.StdOutput = windows.Handle(stdoutWrite.Fd())
	startup.StdErr = windows.Handle(devNull.Fd())
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	var processInfo windows.ProcessInformation
	if err := windows.CreateProcess(application, commandLine, nil, nil, true, flags, nil, nil, &startup.StartupInfo, &processInfo); err != nil {
		cleanupPipes()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	_ = stdinRead.Close()
	_ = stdoutWrite.Close()
	_ = devNull.Close()
	proc, err := proxyCommandJob(processInfo.Process)
	if err != nil {
		_ = windows.TerminateProcess(processInfo.Process, 1)
		_ = windows.CloseHandle(processInfo.Thread)
		_ = windows.CloseHandle(processInfo.Process)
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	if _, err := windows.ResumeThread(processInfo.Thread); err != nil {
		_ = proc.kill()
		_ = proc.close()
		_ = windows.CloseHandle(processInfo.Thread)
		_ = windows.CloseHandle(processInfo.Process)
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	if err := windows.CloseHandle(processInfo.Thread); err != nil {
		_ = proc.kill()
		_ = proc.close()
		_ = windows.CloseHandle(processInfo.Process)
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	return proc, stdinWrite, stdoutRead, nil
}

func proxyCommandJob(process windows.Handle) (*proxyCommandProcess, error) {
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
	return &proxyCommandProcess{process: process, job: job}, nil
}

func (p *proxyCommandProcess) kill() error {
	err := windows.TerminateJobObject(p.job, 1)
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		return os.ErrProcessDone
	}
	return err
}

func (p *proxyCommandProcess) wait() error {
	status, err := windows.WaitForSingleObject(p.process, windows.INFINITE)
	if err != nil {
		return err
	}
	if status != windows.WAIT_OBJECT_0 {
		return os.ErrProcessDone
	}
	return nil
}

func (p *proxyCommandProcess) close() error {
	err1 := windows.CloseHandle(p.process)
	err2 := windows.CloseHandle(p.job)
	if err1 != nil {
		return err1
	}
	return err2
}
