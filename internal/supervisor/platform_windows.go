//go:build windows

package supervisor

import (
	"os"
	"os/exec"
	"strings"
)

// defaultShellCommand starts an interactive PowerShell that loads the user's profiles, like a local terminal
// that is not persistent.
func defaultShellCommand(_ []string) []string {
	if shell, err := exec.LookPath("pwsh.exe"); err == nil {
		return []string{shell, "-NoLogo"}
	}
	return []string{"powershell.exe", "-NoLogo"}
}

func sessionEnv(overrides []string) []string {
	return mergedEnv(overrides)
}

func envKeyEqual(existing, key string) bool {
	return strings.EqualFold(existing, key)
}

func killSignalName() string {
	return ""
}

func execSignalName(*os.ProcessState) string {
	return ""
}
