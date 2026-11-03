//go:build windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// scheduleDeferredReplace starts the deferred replacement script via powershell with a hidden window: the script waits for
// this process to exit, then completes the binary switch, and automatically rolls back to the old binary on failure.
func scheduleDeferredReplace(newPath, targetPath, logPath string) error {
	script, err := os.CreateTemp(filepath.Dir(newPath), ".nexterm-replace-*.ps1")
	if err != nil {
		return err
	}
	scriptPath := script.Name()
	body := renderReplaceScriptWindows(windowsReplaceScript{
		NewPath: newPath, TargetPath: targetPath, WaitPID: os.Getpid(), WaitTimeoutSeconds: replaceWaitTimeoutSeconds,
		LogPath: logPath, RestartBlock: windowsRestartBlock(windowsTaskName),
	})
	if _, err := script.WriteString(body); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		return err
	}
	if err := script.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	cmd, err := startReplaceScript(scriptPath, logPath)
	if err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

const (
	createNoWindow         = 0x08000000
	createBreakawayFromJob = 0x01000000
	breakawayFallbackNote  = "breakaway from job refused; script stays in the agent's job"
)

// startReplaceScript prefers CREATE_BREAKAWAY_FROM_JOB: Task Scheduler tracks a task instance with a job object, and a script left
// in that job can keep the instance looking alive after the agent exits, so the restart block could only time out. When the
// job forbids breakaway, CreateProcess is refused and the script starts without the flag, relying on the restart block's
// timeout; the fallback is recorded in the upgrade log.
func startReplaceScript(scriptPath, logPath string) (*exec.Cmd, error) {
	build := func(flags uint32) *exec.Cmd {
		cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", scriptPath)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		return cmd
	}
	cmd := build(createNoWindow | createBreakawayFromJob)
	if err := cmd.Start(); err == nil {
		return cmd, nil
	}
	appendUpgradeLog(logPath, breakawayFallbackNote)
	cmd = build(createNoWindow)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}
