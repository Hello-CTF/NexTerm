//go:build unix

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// scheduleDeferredReplace starts the deferred replacement script in an independent session: the script waits for this process to exit,
// completes the binary switch, restarts the service and waits for the new agent to report healthy, rolling back to the old binary
// otherwise. setsid lets the script escape launchd's cleanup of leftover processes in the same process group; systemd cleans up
// by cgroup, where setsid has no effect, so on linux under systemd (INVOCATION_ID present) the script is placed in a separate
// scope cgroup via systemd-run --scope, ensuring it can still finish the replacement and restart the service after the agent unit stops.
func scheduleDeferredReplace(plan replacePlan) (bool, error) {
	script, err := os.CreateTemp(filepath.Dir(plan.NewPath), ".nexterm-replace-*.sh")
	if err != nil {
		return false, err
	}
	scriptPath := script.Name()
	body := renderReplaceScriptUnix(plan, unixRestartCommand(runtime.GOOS))
	if _, err := script.WriteString(body); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		return false, err
	}
	if err := script.Chmod(0o755); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		return false, err
	}
	if err := script.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return false, err
	}
	cmd := exec.Command("/bin/sh", scriptPath)
	if runtime.GOOS == "linux" && os.Getenv("INVOCATION_ID") != "" {
		cmd = exec.Command("systemd-run", "--user", "--scope", "--quiet", "--collect", "/bin/sh", scriptPath)
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(scriptPath)
		return false, err
	}
	go func() { _ = cmd.Wait() }()
	return true, nil
}

// prepareStagedCommand needs no adjustment on Unix.
func prepareStagedCommand(*exec.Cmd) {}
