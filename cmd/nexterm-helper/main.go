package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 1 && args[0] == "--metadata" {
		metadata, err := supervisor.CurrentRuntimeMetadata()
		if err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-helper:", err)
			return 1
		}
		if err := json.NewEncoder(os.Stdout).Encode(metadata); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-helper:", err)
			return 1
		}
		return 0
	}
	if len(args) > 0 && args[0] == supervisor.HelperCommand {
		return supervisor.RunHelperCLI(args[1:])
	}
	fmt.Fprintln(os.Stderr, "nexterm-helper: expected supervisor-helper or --metadata")
	return 2
}
