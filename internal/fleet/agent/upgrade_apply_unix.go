//go:build unix

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// scheduleDeferredReplace starts the deferred replacement script in an independent session: the script waits for this process to exit, then
// completes the binary switch, and automatically rolls back to the old binary on failure. setsid lets the script escape launchd's cleanup of
// leftover processes in the same process group; systemd cleans up by cgroup, where setsid has no effect, so on linux under
// systemd (INVOCATION_ID present) the script is placed in a separate scope cgroup via systemd-run --scope, ensuring it can still finish the replacement and restart the service after the agent unit stops.
func scheduleDeferredReplace(newPath, targetPath string) error {
	script, err := os.CreateTemp(filepath.Dir(newPath), ".nexterm-replace-*.sh")
	if err != nil {
		return err
	}
	scriptPath := script.Name()
	body := renderReplaceScriptUnix(newPath, targetPath, replaceScriptDelaySeconds, unixRestartCommand(runtime.GOOS))
	if _, err := script.WriteString(body); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		return err
	}
	if err := script.Chmod(0o755); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		return err
	}
	if err := script.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	cmd := exec.Command("/bin/sh", scriptPath)
	if runtime.GOOS == "linux" && os.Getenv("INVOCATION_ID") != "" {
		cmd = exec.Command("systemd-run", "--user", "--scope", "--quiet", "--collect", "/bin/sh", scriptPath)
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
