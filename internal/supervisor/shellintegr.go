package supervisor

import (
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
