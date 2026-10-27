//go:build windows

package supervisor

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

const helperCreationFlags = windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP

// spawnDetached starts the helper without a console and outside this
// process's job object when the job allows it: a job created with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE (some launchers and terminals use one)
// would otherwise end the helper, and every session in it, when the app
// exits. A job that forbids breakaway rejects the flag with access denied; the
// helper then starts inside the job, as before.
func spawnDetached(executable string, args []string, logPath string) (*helperProcess, error) {
	process, err := spawnDetachedFlags(executable, args, logPath, helperCreationFlags|windows.CREATE_BREAKAWAY_FROM_JOB)
	if err != nil && errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		process, err = spawnDetachedFlags(executable, args, logPath, helperCreationFlags)
		if process != nil {
			process.jobBound = true
		}
	}
	return process, err
}

func spawnDetachedFlags(executable string, args []string, logPath string, flags uint32) (*helperProcess, error) {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return startHelperProcess(cmd, logFile)
}
