//go:build unix

package supervisor

import (
	"log/slog"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/shellenv"
)

const utf8Locale = "C.UTF-8"

// envResolveWait bounds how long creating a session waits for the first login-shell environment
// resolution; after that the best known environment is used.
const envResolveWait = 3 * time.Second

// sessionEnv returns the environment of a new session: the process environment overlaid with the user's
// resolved login-shell environment, then the caller's overrides. The helper is long-lived, so this is
// evaluated on every create and never reuses the environment the helper was started with.
func sessionEnv(overrides []string) []string {
	environment := shellenv.Default().Env(envResolveWait)
	for _, entry := range overrides {
		if key, value, found := strings.Cut(entry, "="); found {
			environment = setEnvironment(environment, key, value)
		}
	}
	if environmentValue(environment, "TERM") == "" {
		environment = setEnvironment(environment, "TERM", "xterm-256color")
	}
	if environmentValue(environment, "COLORTERM") == "" {
		environment = setEnvironment(environment, "COLORTERM", "truecolor")
	}
	if localeMissing(environment) {
		environment = setEnvironment(environment, "LANG", utf8Locale)
	}
	return environment
}

func localeMissing(environment []string) bool {
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if strings.TrimSpace(environmentValue(environment, key)) != "" {
			return false
		}
	}
	return true
}

func setEnvironment(environment []string, key, value string) []string {
	out := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if existing, _, found := strings.Cut(entry, "="); !found || existing != key {
			out = append(out, entry)
		}
	}
	return append(out, key+"="+value)
}

// defaultShell reports the shell to run and whether it takes login-shell flags. The order is a SHELL in the
// session's own environment (the caller asked), the explicit NexTerm setting, the account's login shell,
// the inherited $SHELL, then /bin/sh, which may be dash and therefore gets no -l.
func defaultShell(environment []string) (shell string, login bool) {
	choice := shellenv.Default().Shell(environmentValue(environment, "SHELL"))
	slog.Info("session shell chosen", "shell", choice.Path, "source", choice.Source, "note", choice.Note)
	return choice.Path, choice.Login
}

func defaultShellCommand(environment []string) []string {
	shell, login := defaultShell(environment)
	if !login {
		return []string{shell}
	}
	return []string{shell, "-l"}
}

func envKeyEqual(existing, key string) bool {
	return existing == key
}

func killSignalName() string {
	return "SIGKILL"
}

func execSignalName(state *os.ProcessState) string {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return status.Signal().String()
}
