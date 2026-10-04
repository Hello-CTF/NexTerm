//go:build windows

package supervisor

import (
	"os/exec"
	"strings"
)

func defaultShellCommand(_ []string) []string {
	if shell, err := exec.LookPath("pwsh.exe"); err == nil {
		return []string{shell, "-NoLogo", "-NoProfile"}
	}
	return []string{"powershell.exe", "-NoLogo", "-NoProfile"}
}

func envKeyEqual(existing, key string) bool {
	return strings.EqualFold(existing, key)
}

func killSignalName() string {
	return ""
}
