package main

import (
	"os"

	fleetagent "github.com/Hello-CTF/NexTerm/internal/fleet/agent"
)

func main() {
	os.Exit(fleetagent.RunCLI(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}
