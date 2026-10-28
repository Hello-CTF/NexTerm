// Command nx runs NexTerm's build, release and CI tooling: go run ./scripts/nx <command> [args].
package main

import (
	"fmt"
	"os"
	"sort"
)

type command struct {
	summary string
	run     func(args []string) error
}

var commands = map[string]command{}

// register adds a subcommand; each tool file registers itself from init.
func register(name, summary string, run func(args []string) error) {
	if _, dup := commands[name]; dup {
		panic("nx: duplicate command " + name)
	}
	commands[name] = command{summary: summary, run: run}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "nx: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err := cmd.run(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "nx %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

func usage() {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintln(os.Stderr, "usage: go run ./scripts/nx <command> [args]")
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "  %-28s %s\n", name, commands[name].summary)
	}
}
