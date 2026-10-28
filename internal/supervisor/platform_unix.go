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

	"github.com/Hello-CTF/NexTerm/internal/usershell"
)

// accountShell reports the account's login shell; a variable so tests can pin it.
var accountShell = usershell.Lookup

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

// defaultShell reports the shell to run and whether it takes login-shell flags: the /bin/sh
// fallback may be dash, which rejects -l, while an explicit $SHELL keeps the login behavior.
func defaultShell(environment []string) (shell string, login bool) {
	shell = environmentValue(environment, "SHELL")
	if shell == "" {
		shell = strings.TrimSpace(os.Getenv("SHELL"))
	}
	if shell != "" {
		return shell, true
	}
	// launchd and systemd --user services start without SHELL; the account's shell keeps
	// rc files, PATH and prompt colors identical to a normal terminal (macOS /bin/sh is
	// bash 3.2 and prints the zsh deprecation banner).
	if shell = accountShell(); shell != "" {
		return shell, true
	}
	return "/bin/sh", false
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

// resolveLoginPATH reports the user's login-shell PATH with a short timeout; empty means unresolved.
// Service-installed agents (systemd --user, launchd) run with a minimal PATH where CLIs like docker do not
// resolve, while interactive tabs get the full PATH through the login shell.
func resolveLoginPATHImpl() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := append(defaultShellCommand(nil), "-c", `printf %s "$PATH"`)
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	// Without a delay Wait would block on an inherited stdout pipe if the login shell backgrounds a child.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	text := string(out)
	// A chatty rc file must not poison the overlay: a valid PATH is absolute and whitespace-free.
	if err != nil || !strings.HasPrefix(text, "/") || strings.ContainsAny(text, " \t\n\r") {
		return ""
	}
	return mergePATH(os.Getenv("PATH"), text)
}

// mergePATH returns base followed by the entries of extra that base lacks. Only absolute entries are kept,
// duplicates are dropped and order is preserved. The service-installed PATH is the base, so the entries it
// recorded stay first and the login shell only adds what it knows beyond them.
func mergePATH(base, extra string) string {
	seen := map[string]bool{}
	var merged []string
	for _, list := range []string{base, extra} {
		for _, entry := range strings.Split(list, ":") {
			if !strings.HasPrefix(entry, "/") || seen[entry] {
				continue
			}
			seen[entry] = true
			merged = append(merged, entry)
		}
	}
	return strings.Join(merged, ":")
}

var loginPATH = sync.OnceValue(resolveLoginPATHImpl)
