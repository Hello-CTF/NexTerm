package local

import (
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/shellenv"
)

const utf8Locale = "C.UTF-8"

// resolveWait bounds how long a terminal waits for the first login-shell environment resolution.
const resolveWait = 3 * time.Second

func environment(extra map[string]string, terminal bool, term string) []string {
	return environmentFor("", extra, terminal, term)
}

// environmentFor builds the environment of a local terminal or command: the process environment overlaid
// with the one the user's login shell provides (a GUI app launched from Finder starts with a minimal PATH),
// SHELL set to the shell actually run, then the caller's extras.
func environmentFor(shell string, extra map[string]string, terminal bool, term string) []string {
	env := shellenv.Default().Env(resolveWait)
	if runtime.GOOS != "windows" && filepath.IsAbs(shell) {
		env = setEnvironment(env, "SHELL", shell)
	}
	for key, value := range extra {
		env = setEnvironment(env, key, value)
	}
	if terminal {
		if term == "" {
			term = "xterm-256color"
		}
		env = setEnvironment(env, "TERM", term)
		if environmentValue(env, "COLORTERM") == "" {
			env = setEnvironment(env, "COLORTERM", "truecolor")
		}
	} else if runtime.GOOS != "windows" && environmentValue(env, "TERM") == "" {
		env = setEnvironment(env, "TERM", "xterm-256color")
	}
	if localeMissing(env) {
		env = setEnvironment(env, "LANG", utf8Locale)
	}
	return env
}

func localeMissing(env []string) bool {
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if strings.TrimSpace(environmentValue(env, key)) != "" {
			return false
		}
	}
	return true
}

func environmentValue(env []string, key string) string {
	value := ""
	for _, entry := range env {
		name, current, ok := strings.Cut(entry, "=")
		if ok && environmentKeyEqual(name, key) {
			value = current
		}
	}
	return value
}

func setEnvironment(env []string, key, value string) []string {
	prefix := key + "="
	out := env[:0]
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || !environmentKeyEqual(name, key) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

func environmentKeyEqual(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
