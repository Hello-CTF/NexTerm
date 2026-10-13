//go:build windows

package supervisor

import (
	"os"
	"os/exec"
	"strings"
	"sync"
)

func defaultShellCommand(_ []string) []string {
	if shell, err := exec.LookPath("pwsh.exe"); err == nil {
		return []string{shell, "-NoLogo", "-NoProfile"}
	}
	return []string{"powershell.exe", "-NoLogo", "-NoProfile"}
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

// resolveLoginPATHImpl is a no-op on Windows: the PATH an agent process inherits already covers child processes.
func resolveLoginPATHImpl() string { return "" }

var loginPATH = sync.OnceValue(resolveLoginPATHImpl)
