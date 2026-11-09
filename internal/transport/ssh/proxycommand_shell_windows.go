//go:build windows

package ssh

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type proxyCommandProcess struct {
	process    windows.Handle
	job        windows.Handle
	stderr     *os.File
	stderrDone chan struct{}
	exit       int
}

func startProxyCommand(command string, stderr io.Writer) (*proxyCommandProcess, io.WriteCloser, io.ReadCloser, error) {
	executable, err := proxyCommandExecutable()
	if err != nil {
		return nil, nil, nil, proxyCommandStartError(err)
	}
	application, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return nil, nil, nil, proxyCommandStartError(err)
	}
	commandLine, err := windows.UTF16PtrFromString(proxyCommandLine(executable, command))
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
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stderrRead.Close()
		_ = stdoutWrite.Close()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	cleanupPipes := func() {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stderrRead.Close()
		_ = stdoutWrite.Close()
		_ = stderrRead.Close()
		_ = stderrWrite.Close()
	}
	for _, file := range []*os.File{stdinRead, stdoutWrite, stderrWrite} {
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
	startup.StdErr = windows.Handle(stderrWrite.Fd())
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	var processInfo windows.ProcessInformation
	if err := windows.CreateProcess(application, commandLine, nil, nil, true, flags, nil, nil, &startup.StartupInfo, &processInfo); err != nil {
		cleanupPipes()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	_ = stdinRead.Close()
	_ = stdoutWrite.Close()
	_ = stderrWrite.Close()
	proc, err := proxyCommandJob(processInfo.Process)
	if err != nil {
		_ = windows.TerminateProcess(processInfo.Process, 1)
		_ = windows.CloseHandle(processInfo.Thread)
		_ = windows.CloseHandle(processInfo.Process)
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stderrRead.Close()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	if _, err := windows.ResumeThread(processInfo.Thread); err != nil {
		_ = proc.kill()
		_ = proc.close()
		_ = windows.CloseHandle(processInfo.Thread)
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stderrRead.Close()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	if err := windows.CloseHandle(processInfo.Thread); err != nil {
		_ = proc.kill()
		_ = proc.close()
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stderrRead.Close()
		return nil, nil, nil, proxyCommandStartError(err)
	}
	proc.stderr = stderrRead
	proc.stderrDone = make(chan struct{})
	go func() {
		defer close(proc.stderrDone)
		_, _ = io.Copy(stderr, stderrRead)
	}()
	return proc, stdinWrite, stdoutRead, nil
}

func proxyCommandExecutable() (string, error) {
	executable, err := exec.LookPath("cmd")
	if err == nil {
		return executable, nil
	}
	if comspec := os.Getenv("ComSpec"); comspec != "" {
		return comspec, nil
	}
	return "", err
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
	return &proxyCommandProcess{process: process, job: job, exit: -1}, nil
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
	var code uint32
	if err := windows.GetExitCodeProcess(p.process, &code); err == nil {
		p.exit = int(code)
	}
	if p.stderrDone != nil {
		timer := time.NewTimer(proxyCommandStderrWait)
		defer timer.Stop()
		select {
		case <-p.stderrDone:
		case <-timer.C:
		}
	}
	return nil
}

// exitCode is valid after wait returns; -1 means the exit code is unknown.
func (p *proxyCommandProcess) exitCode() int {
	return p.exit
}

func (p *proxyCommandProcess) close() error {
	if p.stderr != nil {
		_ = p.stderr.Close()
	}
	err1 := windows.CloseHandle(p.process)
	err2 := windows.CloseHandle(p.job)
	if err1 != nil {
		return err1
	}
	return err2
}
