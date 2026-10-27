//go:build unix

package supervisor

import (
	"os"
	"os/exec"
	"syscall"
)

// spawnDetached starts the helper in its own session, so neither the end of
// this process nor a terminal hangup reaches it, and sets no parent-death
// signal.
func spawnDetached(executable string, args []string, logPath string) (*helperProcess, error) {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return startHelperProcess(cmd, logFile)
}
