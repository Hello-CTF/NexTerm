package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/version"
)

type helperMetadata struct {
	Version  string   `json:"version"`
	Commit   string   `json:"commit"`
	Protocol int      `json:"protocol"`
	Features []string `json:"features"`
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 1 && args[0] == "--metadata" {
		if err := json.NewEncoder(os.Stdout).Encode(helperMetadata{
			Version:  version.Version,
			Commit:   version.Commit,
			Protocol: supervisor.ProtocolVersion,
			Features: []string{"exec"},
		}); err != nil {
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
