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
func scheduleDeferredReplace(newPath, targetPath string) error {
	script, err := os.CreateTemp(filepath.Dir(newPath), ".nexterm-replace-*.ps1")
	if err != nil {
		return err
	}
	scriptPath := script.Name()
	body := renderReplaceScriptWindows(newPath, targetPath, replaceScriptDelaySeconds)
	if _, err := script.WriteString(body); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		return err
	}
	if err := script.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
