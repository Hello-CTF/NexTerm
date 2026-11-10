//go:build windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// scheduleDeferredReplace starts the deferred replacement script via powershell with a hidden window: the script waits for
// this process to exit, completes the binary switch, restarts the task and waits for the new agent to report healthy,
// rolling back to the old binary otherwise.
func scheduleDeferredReplace(plan replacePlan) (bool, error) {
	body := renderReplaceScriptWindows(newWindowsReplaceScript(plan, windowsTaskName))
	scriptPath, err := writeReplaceScriptFile(filepath.Dir(plan.NewPath), body)
	if err != nil {
		return false, err
	}
	cmd, detached, err := startReplaceScript(scriptPath, plan.LogPath)
	if err != nil {
		_ = os.Remove(scriptPath)
		return false, err
	}
	go func() { _ = cmd.Wait() }()
	return detached, nil
}

// prepareStagedCommand keeps the staged binary's self-check from opening a console window.
func prepareStagedCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

const (
	createNoWindow         = 0x08000000
	createBreakawayFromJob = 0x01000000
	breakawayFallbackNote  = "breakaway from job refused; script stays in the agent's job"
)

// startReplaceScript prefers CREATE_BREAKAWAY_FROM_JOB: Task Scheduler tracks a task instance with a job object, and a script left
// in that job can keep the instance looking alive after the agent exits, so the restart block could only time out. When the
// job forbids breakaway, CreateProcess is refused and the script starts without the flag, relying on the restart block's
// timeout; the fallback is recorded in the upgrade log. The bool reports whether the script left the job.
func startReplaceScript(scriptPath, logPath string) (*exec.Cmd, bool, error) {
	build := func(flags uint32) *exec.Cmd {
		cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", scriptPath)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		return cmd
	}
	cmd := build(createNoWindow | createBreakawayFromJob)
	if err := cmd.Start(); err == nil {
		return cmd, true, nil
	}
	appendUpgradeLog(logPath, breakawayFallbackNote)
	cmd = build(createNoWindow)
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	return cmd, false, nil
}
