//go:build unix

package supervisor

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const utf8Locale = "C.UTF-8"

func sessionEnv(overrides []string) []string {
	environment := mergedEnv(overrides)
	if environment == nil {
		environment = os.Environ()
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
	out := environment[:0]
	for _, entry := range environment {
		if existing, _, found := strings.Cut(entry, "="); !found || existing != key {
			out = append(out, entry)
		}
	}
	return append(out, key+"="+value)
}

func defaultShellCommand(environment []string) []string {
	shell := environmentValue(environment, "SHELL")
	if shell == "" {
		shell = strings.TrimSpace(os.Getenv("SHELL"))
	}
	if shell == "" {
		shell = "/bin/sh"
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

// resolveLoginPATH reports the user's login-shell PATH with a short timeout; empty means unresolved.
// Service-installed agents (systemd --user, launchd) run with a minimal PATH where CLIs like docker do not
// resolve, while interactive tabs get the full PATH through the login shell.
func resolveLoginPATHImpl() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, defaultShellCommand(nil)[0], "-l", "-c", `printf %s "$PATH"`)
	// Without a delay Wait would block on an inherited stdout pipe if the login shell backgrounds a child.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	text := string(out)
	// A chatty rc file must not poison the overlay: a valid PATH is absolute and whitespace-free.
	if err != nil || !strings.HasPrefix(text, "/") || strings.ContainsAny(text, " \t\n\r") {
		return ""
	}
	return text
}

var loginPATH = sync.OnceValue(resolveLoginPATHImpl)
