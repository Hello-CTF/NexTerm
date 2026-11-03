//go:build windows

package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// exitedPID returns the PID of a process that has already exited, so Wait-Process returns at once.
func exitedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("cmd.exe", "/c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestReplaceScriptWindowsExecutes(t *testing.T) {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		t.Skip("powershell.exe unavailable")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "nexterm-agent.exe")
	newPath := filepath.Join(dir, ".nexterm-agent.exe.nexterm-new")
	marker := filepath.Join(dir, "restarted.marker")
	logPath := filepath.Join(dir, "upgrade.log")
	pid := exitedPID(t)

	run := func(t *testing.T) string {
		t.Helper()
		_ = os.Remove(marker)
		scriptPath := filepath.Join(dir, "replace.ps1")
		body := renderReplaceScriptWindows(windowsReplaceScript{
			NewPath: newPath, TargetPath: target, WaitPID: pid, WaitTimeoutSeconds: 5, LogPath: logPath,
			RestartBlock: "Set-Content -LiteralPath " + psQuote(marker) + " -Value ok",
		})
		if err := os.WriteFile(scriptPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		output, err := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath).CombinedOutput()
		if err != nil {
			t.Fatalf("script failed: %v\n%s", err, output)
		}
		return scriptPath
	}

	t.Run("successful swap restarts and cleans up", func(t *testing.T) {
		if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(newPath, []byte("new-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		scriptPath := run(t)
		content, err := os.ReadFile(target)
		if err != nil || string(content) != "new-binary" {
			t.Fatalf("target = %q, %v", content, err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("restart block did not run: %v", err)
		}
		if _, err := os.Stat(target + ".nexterm-old"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("backup must be removed after a successful swap")
		}
		if _, err := os.Stat(scriptPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("script must remove itself")
		}
	})

	t.Run("failed swap rolls back without restarting", func(t *testing.T) {
		if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(newPath)
		run(t)
		content, err := os.ReadFile(target)
		if err != nil || strings.TrimSpace(string(content)) != "old-binary" {
			t.Fatalf("rollback must restore the old binary, target = %q, %v", content, err)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("rollback must not run the restart block")
		}
	})
}
