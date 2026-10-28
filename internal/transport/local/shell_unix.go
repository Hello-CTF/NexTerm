//go:build unix

package local

import (
	"os"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/usershell"
)

// accountShell reports the account's login shell; a variable so tests can pin it.
var accountShell = usershell.Lookup

// defaultShell reports the shell to run and whether it takes login-shell flags: the /bin/sh
// fallback may be dash, which rejects -l, while an explicit $SHELL keeps the login behavior.
func defaultShell() (shell string, login bool) {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell, true
	}
	if shell := accountShell(); shell != "" {
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
