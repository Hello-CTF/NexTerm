//go:build windows

package local

import "os/exec"

func defaultShell() string {
	if shell, err := exec.LookPath("pwsh.exe"); err == nil {
		return shell
	}
	return "powershell.exe"
}

func shellArguments(command string) []string {
	return []string{"-NoLogo", "-NoProfile", "-Command", command}
}
