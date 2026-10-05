package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/terminal/shellintegr"
)

type shellLaunch struct {
	command []string
	env     []string
	cleanup func()
}

func wrapShellIntegration(command, env []string) (shellLaunch, bool) {
	if len(command) == 0 {
		return shellLaunch{}, false
	}
	shell, err := shellintegr.Detect(command[0])
	if err != nil || !interactiveShellArgs(command[1:]) {
		return shellLaunch{}, false
	}
	launch, err := shellintegr.Wrap(shell, command[0])
	if err != nil {
		return shellLaunch{}, false
	}
	wrapped := shellLaunch{
		command: append([]string{launch.Path}, launch.Args...),
		env:     append(append([]string(nil), env...), launch.Env...),
		cleanup: launch.Cleanup,
	}
	if loginShellArgs(command[1:]) {
		if err := preserveLoginMode(shell, launch, &wrapped); err != nil {
			launch.Cleanup()
			return shellLaunch{}, false
		}
	}
	return wrapped, true
}

func interactiveShellArgs(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-l", "-i", "--login", "-li", "-il":
		default:
			return false
		}
	}
	return true
}

func loginShellArgs(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-l", "--login", "-li", "-il":
			return true
		}
	}
	return false
}

// preserveLoginMode restores the original launch's login semantics. zsh and
// fish take a real login flag alongside the wrapper. bash only honors
// --rcfile for non-login shells, so the login startup chain is sourced from a
// composite rcfile that then loads the wrapper.
func preserveLoginMode(shell shellintegr.Shell, launch *shellintegr.Launch, wrapped *shellLaunch) error {
	switch shell {
	case shellintegr.ShellZsh:
		wrapped.command = []string{launch.Path, "-l", "-i"}
	case shellintegr.ShellFish:
		wrapped.command = append([]string{launch.Path, "-l"}, launch.Args...)
	case shellintegr.ShellBash:
		wrapperPath, err := rcfilePath(launch.Args)
		if err != nil {
			return err
		}
		composite := filepath.Join(filepath.Dir(wrapperPath), "login-rc")
		if err := os.WriteFile(composite, []byte(bashLoginChain(wrapperPath)), 0o600); err != nil {
			return err
		}
		wrapped.command = []string{launch.Path, "--rcfile", composite, "-i"}
	}
	return nil
}

func rcfilePath(args []string) (string, error) {
	for index, arg := range args {
		if arg == "--rcfile" && index+1 < len(args) {
			return args[index+1], nil
		}
	}
	return "", fmt.Errorf("wrapped bash launch carries no --rcfile")
}

func bashLoginChain(wrapperPath string) string {
	quoted := "'" + strings.ReplaceAll(wrapperPath, "'", `'"'"'`) + "'"
	return `[[ -r /etc/profile ]] && source /etc/profile
if [[ -r $HOME/.bash_profile ]]; then
    source $HOME/.bash_profile
elif [[ -r $HOME/.bash_login ]]; then
    source $HOME/.bash_login
elif [[ -r $HOME/.profile ]]; then
    source $HOME/.profile
fi
source ` + quoted + "\n"
}
