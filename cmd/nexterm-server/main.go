package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/platform"
	"github.com/ProbiusOfficial/NexTerm/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	invocation, err := core.ParseCLI(args, core.CommandServe, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 2
	}
	if invocation.Help {
		fmt.Fprint(os.Stdout, core.Usage("nexterm-server", core.CommandServe))
		return 0
	}
	if invocation.Version {
		fmt.Fprintln(os.Stdout, version.Version)
		return 0
	}

	switch invocation.Command {
	case core.CommandToken, core.CommandRotateToken:
		if err := core.RunTokenCommand(context.Background(), invocation.Command, nil, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", err)
			return 1
		}
		return 0
	case core.CommandDesktop:
		fmt.Fprintln(os.Stderr, "nexterm-server: use the nexterm-desktop binary for the desktop command")
		return 2
	case core.CommandServe:
	default:
		fmt.Fprintln(os.Stderr, "nexterm-server: unsupported command", invocation.Command)
		return 2
	}

	paths, err := platform.ServerPaths(invocation.DataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	if err := paths.Prepare(); err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	logger, logErr := platform.NewLogger(paths, os.Stderr)
	if logErr != nil {
		logger.Warn("file logging is unavailable; using console only", "error", logErr)
	}
	defer func() {
		if err := logger.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server: close log:", err)
		}
	}()

	application, err := core.New(core.Config{Logger: logger.Logger})
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	ctx, stop := platform.NotifyContext(context.Background())
	defer stop()
	if err := application.Serve(ctx, core.ServeConfig{
		Listen:   invocation.Listen,
		WebRoot:  invocation.WebRoot,
		SyncOnly: invocation.SyncOnly,
	}); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("server stopped", "error", err)
		return 1
	}
	return 0
}
