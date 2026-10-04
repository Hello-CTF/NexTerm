//go:build unix

package supervisor

import (
	"os"
	"strings"
)

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
