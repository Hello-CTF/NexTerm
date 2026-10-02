package local

import (
	"os"
	"runtime"
	"strings"
)

const utf8Locale = "C.UTF-8"

func environment(extra map[string]string, terminal bool, term string) []string {
	env := append([]string(nil), os.Environ()...)
	for key, value := range extra {
		env = setEnvironment(env, key, value)
	}
	if terminal {
		if term == "" {
			term = "xterm-256color"
		}
		env = setEnvironment(env, "TERM", term)
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
