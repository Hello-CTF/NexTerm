//go:build windows

package pty

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"unicode/utf16"
	"unsafe"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"golang.org/x/sys/windows"
)

type windowsProcessTree struct {
	process windows.Handle
	job     windows.Handle
}

type windowsStartOptions struct {
	attributes  *windows.ProcThreadAttributeListContainer
	afterCreate func() error
	conPTY      bool
	noWindow    bool
}

func startProcess(cmd *exec.Cmd) (*Process, error) {
	return startWindowsProcess(cmd, windowsStartOptions{noWindow: true})
}

func startWindowsProcess(cmd *exec.Cmd, options windowsStartOptions) (*Process, error) {
	application, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		return nil, err
	}
	commandLine, err := windows.UTF16PtrFromString(windowsCommandLine(cmd))
	if err != nil {
		return nil, err
	}
	var directory *uint16
	if cmd.Dir != "" {
		directory, err = windows.UTF16PtrFromString(cmd.Dir)
		if err != nil {
			return nil, err
		}
	}
	environment := windowsEnvironmentBlock(cmd.Env)
	startup := new(windows.StartupInfoEx)
	startup.Flags = windows.STARTF_USESTDHANDLES
	inherit := false
	if !options.conPTY {
		inherit = true
		if err := windowsProcessHandles(cmd, &startup.StartupInfo); err != nil {
			return nil, err
		}
	}
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP)
	if options.noWindow {
		flags |= windows.CREATE_NO_WINDOW
	}
	startup.Cb = uint32(unsafe.Sizeof(startup.StartupInfo))
	if options.attributes != nil {
		startup.Cb = uint32(unsafe.Sizeof(*startup))
		startup.ProcThreadAttributeList = options.attributes.List()
		flags |= windows.EXTENDED_STARTUPINFO_PRESENT
	}
	var processInfo windows.ProcessInformation
	err = windows.CreateProcess(
		application,
		commandLine,
		nil,
		nil,
		inherit,
		flags,
		&environment[0],
		directory,
		&startup.StartupInfo,
		&processInfo,
	)
	if err != nil {
		return nil, err
	}
	tree, err := newWindowsProcessTree(processInfo.Process)
	if err != nil {
		_ = windows.TerminateProcess(processInfo.Process, 1)
		_ = windows.CloseHandle(processInfo.Thread)
		_ = windows.CloseHandle(processInfo.Process)
		return nil, err
	}
	if options.afterCreate != nil {
		if err := options.afterCreate(); err != nil {
			_ = tree.Kill()
			_ = tree.Close()
			_ = windows.CloseHandle(processInfo.Thread)
			return nil, err
		}
	}
	if _, err := windows.ResumeThread(processInfo.Thread); err != nil {
		_ = tree.Kill()
		_ = tree.Close()
		_ = windows.CloseHandle(processInfo.Thread)
		return nil, err
	}
	if err := windows.CloseHandle(processInfo.Thread); err != nil {
		_ = tree.Kill()
		_ = tree.Close()
		return nil, err
	}
	pid := int(processInfo.ProcessId)
	return newProcess(pid, tree, func() error {
		status, err := windows.WaitForSingleObject(tree.process, windows.INFINITE)
		if err != nil {
			return err
		}
		if status != windows.WAIT_OBJECT_0 {
			return fmt.Errorf("wait for process %d: status %d", pid, status)
		}
		var code uint32
		if err := windows.GetExitCodeProcess(tree.process, &code); err != nil {
			return err
		}
		if code != 0 {
			return &base.ExitError{Code: int(int32(code))}
		}
		return nil
	}), nil
}

func windowsProcessHandles(cmd *exec.Cmd, startup *windows.StartupInfo) error {
	for _, stream := range []struct {
		name   string
		value  any
		target *windows.Handle
	}{
		{"stdin", cmd.Stdin, &startup.StdInput},
		{"stdout", cmd.Stdout, &startup.StdOutput},
		{"stderr", cmd.Stderr, &startup.StdErr},
	} {
		file, ok := stream.value.(*os.File)
		if !ok || file == nil {
			return fmt.Errorf("Windows process %s must be an *os.File", stream.name)
		}
		handle := windows.Handle(file.Fd())
		if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return err
		}
		*stream.target = handle
	}
	return nil
}

func windowsCommandLine(cmd *exec.Cmd) string {
	args := append([]string(nil), cmd.Args...)
	if len(args) == 0 {
		args = []string{cmd.Path}
	} else {
		args[0] = cmd.Path
	}
	return windows.ComposeCommandLine(args)
}

func windowsEnvironmentBlock(env []string) []uint16 {
	if env == nil {
		env = os.Environ()
	} else {
		env = append([]string(nil), env...)
	}
	hasSystemRoot := false
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, "SYSTEMROOT") {
			hasSystemRoot = true
			break
		}
	}
	if !hasSystemRoot {
		env = append(env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"))
	}
	sort.Slice(env, func(i, j int) bool {
		return strings.ToUpper(env[i]) < strings.ToUpper(env[j])
	})
	return utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
}

func newProcessTree(process *os.Process) (processTree, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(process.Pid))
	if err != nil {
		return nil, err
	}
	tree, err := newWindowsProcessTree(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return tree, nil
}

func newWindowsProcessTree(process windows.Handle) (*windowsProcessTree, error) {
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
	return &windowsProcessTree{process: process, job: job}, nil
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
