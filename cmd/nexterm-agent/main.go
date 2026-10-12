package main

import (
	"os"

	fleetagent "github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == supervisor.HelperCommand {
		return supervisor.RunHelperCLI(args[1:])
	}
	return fleetagent.RunCLI(args, os.Getenv, os.Stdout, os.Stderr)
}
