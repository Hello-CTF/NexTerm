package docker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func (b *CommandBackend) ProbeCompose(ctx context.Context, cli ComposeCLI) error {
	_, err := b.runComposeResult(ctx, cli, []string{"version"})
	return err
}

func (b *CommandBackend) CheckComposeFiles(ctx context.Context, stack ComposeStack) (ComposeFilesStatus, error) {
	var status ComposeFilesStatus
	if stack.WorkingDir == "" {
		return status, errors.New("compose working directory is empty")
	}
	exists, err := b.pathExists(ctx, stack.WorkingDir, true)
	if err != nil {
		return status, err
	}
	if !exists {
		status.Missing = append(status.Missing, stack.WorkingDir)
	}
	for _, file := range stack.ConfigFiles {
		exists, err = b.pathExists(ctx, file, false)
		if err != nil {
			return status, err
		}
		if !exists {
			status.Missing = append(status.Missing, file)
		}
	}
	if stack.EnvPath != "" {
		status.EnvExists, err = b.pathExists(ctx, stack.EnvPath, false)
		if err != nil {
			return status, err
		}
	}
	return status, nil
}

func (b *CommandBackend) pathExists(ctx context.Context, value string, directory bool) (bool, error) {
	if b.runner == nil {
		return false, fmt.Errorf("%w: command runner", ErrNoBackend)
	}
	pathType := "Leaf"
	test := "-f"
	if directory {
		pathType = "Container"
		test = "-d"
	}
	command := "test " + test + " " + quoteShell(value, b.shell)
	if b.shell == ShellPowerShell {
		command = "if (Test-Path -LiteralPath " + quoteShell(value, b.shell) + " -PathType " + pathType + ") { exit 0 } else { exit 1 }"
	}
	result, err := b.runner.RunCommand(ctx, command)
	if err != nil {
		return false, err
	}
	if result.Truncated {
		return false, errors.New("compose file probe output exceeded the transport limit")
	}
	return result.ExitCode == nil || *result.ExitCode == 0, nil
}

func (b *CommandBackend) RunCompose(ctx context.Context, cli ComposeCLI, stack ComposeStack, action ComposeAction) (string, error) {
	args, err := composeActionArgs(stack, action, 0)
	if err != nil {
		return "", err
	}
	result, err := b.runComposeResult(ctx, cli, args)
	if err != nil {
		return result.Stdout + result.Stderr, err
	}
	return result.Stdout + result.Stderr, nil
}

func (b *CommandBackend) OpenCompose(ctx context.Context, cli ComposeCLI, stack ComposeStack, action ComposeAction, tail int) (LogStream, error) {
	args, err := composeActionArgs(stack, action, tail)
	if err != nil {
		return LogStream{}, err
	}
	stream, err := b.open(ctx, b.composeCommand(cli, args), 120, 40)
	if err != nil {
		return LogStream{}, err
	}
	return LogStream{Reader: combinedCommandOutput(stream), TTY: true}, nil
}

func composeActionArgs(stack ComposeStack, action ComposeAction, tail int) ([]string, error) {
	if stack.Project == "" || stack.WorkingDir == "" || len(stack.ConfigFiles) == 0 {
		return nil, errors.New("compose stack metadata is incomplete")
	}
	args := []string{"--project-directory", stack.WorkingDir}
	for _, file := range stack.ConfigFiles {
		args = append(args, "--file", file)
	}
	args = append(args, "--project-name", stack.Project, string(action))
	switch action {
	case ComposeActionUp:
		args = append(args, "--detach")
	case ComposeActionLogs:
		if tail < 0 || tail > 5000 {
			return nil, errors.New("compose logs tail must be between 0 and 5000")
		}
		args = append(args, "--follow", "--tail", strconv.Itoa(tail), "--no-color")
	case ComposeActionDown, ComposeActionRestart, ComposeActionPull:
	default:
		return nil, fmt.Errorf("%w: compose %s", ErrInvalidAction, action)
	}
	return args, nil
}

func (b *CommandBackend) runComposeResult(ctx context.Context, cli ComposeCLI, args []string) (CommandResult, error) {
	if b.runner == nil {
		return CommandResult{}, fmt.Errorf("%w: command runner", ErrNoBackend)
	}
	result, err := b.runner.RunCommand(ctx, b.composeCommand(cli, args))
	if err != nil {
		return CommandResult{}, err
	}
	if result.Truncated {
		return CommandResult{}, errors.New("compose command output exceeded the transport limit")
	}
	if result.ExitCode != nil && *result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = strings.TrimSpace(result.Stdout)
		}
		return result, fmt.Errorf("compose command exited with code %d: %s", *result.ExitCode, message)
	}
	return result, nil
}

func (b *CommandBackend) composeCommand(cli ComposeCLI, args []string) string {
	if cli == ComposeCLILegacy {
		return b.commandWithBinary("docker-compose", args)
	}
	return b.commandWithBinary(b.binary, append([]string{"compose"}, args...))
}

func (b *CommandBackend) commandWithBinary(binary string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, binary)
	for _, arg := range args {
		parts = append(parts, quoteShell(arg, b.shell))
	}
	return strings.Join(parts, " ")
}
