//go:build unix

package local

import (
	"os"
	"strings"
)

// defaultShell reports the shell to run and whether it takes login-shell flags: the /bin/sh
// fallback may be dash, which rejects -l, while an explicit $SHELL keeps the login behavior.
func defaultShell() (shell string, login bool) {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell, true
	}
	return "/bin/sh", false
}

func shellArguments(login bool, command string) []string {
	if !login {
		return []string{"-c", command}
	}
	return []string{"-lc", command}
}
