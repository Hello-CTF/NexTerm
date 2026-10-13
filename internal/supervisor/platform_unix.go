//go:build unix

package supervisor

import (
	"os"
	"strings"
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
