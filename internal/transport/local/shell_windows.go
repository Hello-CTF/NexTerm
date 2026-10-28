//go:build windows

package local

import "os/exec"

func defaultShell() (shell string, login bool) {
	if shell, err := exec.LookPath("pwsh.exe"); err == nil {
		return shell, true
	}
	return "powershell.exe", true
}

func shellArguments(_ bool, command string) []string {
	return []string{"-NoLogo", "-NoProfile", "-Command", command}
}

func defaultShellChoice() (shell string, login bool, source, note string) {
	shell, login = defaultShell()
	return shell, login, "default", ""
}
