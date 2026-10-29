//go:build unix

package local

import (
	"github.com/Hello-CTF/NexTerm/internal/shellenv"
)

// defaultShell reports the shell to run and whether it takes login-shell flags: the /bin/sh
// fallback may be dash, which rejects -l, while a chosen shell keeps the login behavior.
func defaultShell() (shell string, login bool) {
	shell, login, _, _ = defaultShellChoice()
	return shell, login
}

// defaultShellChoice applies the shared precedence: explicit NexTerm setting, then the account login
// shell, then the inherited $SHELL (GUI apps launched from Finder inherit a stale or empty one), then /bin/sh.
func defaultShellChoice() (shell string, login bool, source, note string) {
	choice := shellenv.Default().Shell("")
	return choice.Path, choice.Login, string(choice.Source), choice.Note
}

func shellArguments(login bool, command string) []string {
	if !login {
		return []string{"-c", command}
	}
	return []string{"-lc", command}
}
