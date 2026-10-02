//go:build unix

package local

import (
	"os"
	"strings"
)

func defaultShell() string {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell
	}
	return "/bin/sh"
}

func shellArguments(command string) []string {
	return []string{"-lc", command}
}
