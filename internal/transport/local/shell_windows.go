//go:build windows

package local

import (
	"os/exec"
	"path/filepath"
	"strings"
)

func defaultShell() (shell string, login bool) {
	return commandShell(), true
}

// commandShell is the PowerShell that runs exec commands: pwsh when installed, else Windows PowerShell.
func commandShell() string {
	if shell, err := exec.LookPath("pwsh.exe"); err == nil {
		return shell
	}
	return "powershell.exe"
}

// commandInvocation runs command in PowerShell whatever the interactive shell is: exec callers (process
// list, port probes, the Docker panel, AI commands) write PowerShell for a Windows host, which cmd.exe,
// bash.exe or wsl.exe would misread. A PowerShell chosen as the interactive shell is kept.
func commandInvocation(shell string, _ bool, command string) (string, []string) {
	if !powerShellExecutable(shell) {
		shell = commandShell()
	}
	return shell, []string{"-NoLogo", "-NoProfile", "-Command", command}
}

func powerShellExecutable(shell string) bool {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(strings.TrimSpace(shell))), ".exe")
	return name == "pwsh" || name == "powershell"
}

func defaultShellChoice() (shell string, login bool, source, note string) {
	shell, login = defaultShell()
	return shell, login, "default", ""
}
