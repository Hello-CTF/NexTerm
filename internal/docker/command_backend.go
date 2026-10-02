package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
)

type CommandResult struct {
	Stdout    string
	Stderr    string
	ExitCode  *int
	Truncated bool
}

type CommandRunner interface {
	RunCommand(ctx context.Context, command string) (CommandResult, error)
}

type CommandRunnerFunc func(ctx context.Context, command string) (CommandResult, error)

func (f CommandRunnerFunc) RunCommand(ctx context.Context, command string) (CommandResult, error) {
	return f(ctx, command)
}

type CommandStream interface {
	io.ReadWriteCloser
	CloseWrite() error
	Resize(ctx context.Context, width, height uint) error
	Wait(ctx context.Context) (int, error)
}

type CommandOpener interface {
	OpenCommand(ctx context.Context, command string, width, height uint) (CommandStream, error)
}

type Shell int

const (
	ShellPOSIX Shell = iota
	ShellPowerShell
)

type CommandBackend struct {
	runner CommandRunner
	opener CommandOpener
	shell  Shell
	binary string
}

func NewCommandBackend(runner CommandRunner, opener CommandOpener, shell Shell) *CommandBackend {
	return &CommandBackend{runner: runner, opener: opener, shell: shell, binary: "docker"}
}

func (b *CommandBackend) ListContainerDTOs(ctx context.Context, all bool) ([]ContainerSummary, error) {
	args := []string{"ps"}
	if all {
		args = append(args, "-a")
	}
	args = append(args, "--format", "{{json .}}")
	output, err := b.run(ctx, args)
	if err != nil {
		return nil, err
	}
	rows, err := decodeJSONLines[cliContainer](output)
	if err != nil {
		return nil, err
	}
	result := make([]ContainerSummary, 0, len(rows))
	for _, row := range rows {
		var project *string
		if value, ok := cliLabel(row.Labels, "com.docker.compose.project"); ok {
			project = &value
		}
		result = append(result, ContainerSummary{
			ID:             row.ID,
			Name:           row.Names,
			Image:          row.Image,
			State:          row.State,
			Status:         row.Status,
			Ports:          row.Ports,
			ComposeProject: project,
		})
	}
	return result, nil
}

func (b *CommandBackend) ListContainers(context.Context, bool) ([]container.Summary, error) {
	return nil, fmt.Errorf("%w: raw containers through command backend", ErrUnsupported)
}

func (b *CommandBackend) InspectContainer(ctx context.Context, id string) (json.RawMessage, error) {
	output, err := b.run(ctx, []string{"inspect", id})
	if err != nil {
		return nil, err
	}
	var value json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &value); err != nil {
		return nil, fmt.Errorf("decode docker inspect: %w", err)
	}
	var array []json.RawMessage
	if err := json.Unmarshal(value, &array); err == nil {
		if len(array) != 1 {
			return nil, fmt.Errorf("docker inspect returned %d items", len(array))
		}
		return array[0], nil
	}
	return value, nil
}

func (b *CommandBackend) ContainerStats(context.Context, string) (container.StatsResponse, error) {
	return container.StatsResponse{}, fmt.Errorf("%w: raw stats through command backend", ErrUnsupported)
}

func (b *CommandBackend) StatsSnapshot(ctx context.Context) (string, error) {
	return b.run(ctx, []string{"stats", "--no-stream", "--format", "{{json .}}"})
}

func (b *CommandBackend) ListImageDTOs(ctx context.Context) ([]ImageSummary, error) {
	output, err := b.run(ctx, []string{"images", "--format", "{{json .}}"})
	if err != nil {
		return nil, err
	}
	rows, err := decodeJSONLines[cliImage](output)
	if err != nil {
		return nil, err
	}
	result := make([]ImageSummary, 0, len(rows))
	for _, row := range rows {
		result = append(result, ImageSummary{
			ID:           row.ID,
			Repository:   row.Repository,
			Tag:          row.Tag,
			Size:         row.Size,
			CreatedSince: row.CreatedSince,
		})
	}
	return result, nil
}

func (b *CommandBackend) ListImages(context.Context) ([]image.Summary, error) {
	return nil, fmt.Errorf("%w: raw images through command backend", ErrUnsupported)
}

func (b *CommandBackend) Action(ctx context.Context, options ActionOptions) error {
	var args []string
	switch options.Action {
	case ActionStart, ActionPause, ActionUnpause, ActionKill:
		args = []string{string(options.Action), options.Container}
	case ActionStop, ActionRestart:
		args = []string{string(options.Action)}
		if options.StopTimeout != nil {
			args = append(args, "--time", strconv.Itoa(*options.StopTimeout))
		}
		args = append(args, options.Container)
	case ActionRemove:
		args = []string{"rm"}
		if options.Force {
			args = append(args, "--force")
		}
		args = append(args, options.Container)
	case ActionRename:
		args = []string{"rename", options.Container, options.NewName}
	default:
		return fmt.Errorf("%w: %s", ErrInvalidAction, options.Action)
	}
	_, err := b.run(ctx, args)
	return err
}

func (b *CommandBackend) PullImage(ctx context.Context, options PullOptions) (string, error) {
	if options.RegistryAuth != "" {
		return "", errors.New("command pull uses the remote Docker credential configuration")
	}
	return b.run(ctx, []string{"pull", options.Reference})
}

func (b *CommandBackend) RemoveImage(ctx context.Context, reference string, force bool) error {
	args := []string{"rmi"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, reference)
	_, err := b.run(ctx, args)
	return err
}

func (b *CommandBackend) OpenLogs(ctx context.Context, options LogsOptions) (LogStream, error) {
	tail := "all"
	if options.Tail >= 0 {
		tail = strconv.Itoa(options.Tail)
	}
	args := []string{"logs"}
	if options.Follow {
		args = append(args, "--follow")
	}
	args = append(args, "--tail", tail)
	if options.Since != "" {
		args = append(args, "--since", options.Since)
	}
	args = append(args, options.Container)
	if !options.Follow {
		result, err := b.runResult(ctx, args)
		if err != nil {
			return LogStream{}, err
		}
		return LogStream{Reader: io.NopCloser(strings.NewReader(result.Stdout + result.Stderr)), TTY: true}, nil
	}
	stream, err := b.open(ctx, b.command(args), 120, 40)
	if err != nil {
		return LogStream{}, err
	}
	return LogStream{Reader: combinedCommandOutput(stream), TTY: true}, nil
}

func (b *CommandBackend) OpenExec(ctx context.Context, options ExecOptions) (ExecSession, error) {
	stream, err := b.open(ctx, b.command(commandExecArgs(options, true)), options.Width, options.Height)
	if err != nil {
		return nil, err
	}
	return newCommandExecSession(stream), nil
}

func (b *CommandBackend) ExecOnce(ctx context.Context, options ExecOptions) (ExecResult, error) {
	if b.runner == nil {
		return execWithSession(ctx, b, options)
	}
	result, err := b.runner.RunCommand(ctx, b.command(commandExecArgs(options, false)))
	if err != nil {
		return ExecResult{}, err
	}
	if result.Truncated {
		return ExecResult{}, errors.New("docker command output exceeded the transport limit")
	}
	exitCode := 0
	if result.ExitCode != nil {
		exitCode = *result.ExitCode
	}
	return ExecResult{Output: result.Stdout + result.Stderr, ExitCode: exitCode}, nil
}

func commandExecArgs(options ExecOptions, streaming bool) []string {
	args := []string{"exec"}
	if streaming {
		if options.TTY {
			args = append(args, "-it")
		} else {
			args = append(args, "-i")
		}
	}
	if options.User != "" {
		args = append(args, "--user", options.User)
	}
	for _, env := range options.Env {
		args = append(args, "--env", env)
	}
	if options.WorkDir != "" {
		args = append(args, "--workdir", options.WorkDir)
	}
	args = append(args, options.Container)
	return append(args, options.Cmd...)
}

func (b *CommandBackend) OpenRawExec(ctx context.Context, command string, width, height uint) (ExecSession, error) {
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("docker custom command is required")
	}
	stream, err := b.open(ctx, command, width, height)
	if err != nil {
		return nil, err
	}
	return newCommandExecSession(stream), nil
}

func (b *CommandBackend) ListDir(ctx context.Context, id, directory string) ([]string, error) {
	output, err := b.run(ctx, []string{"exec", id, "sh", "-c", `ls -1a -- "$1"`, "sh", directory})
	if err != nil {
		return nil, err
	}
	lines := strings.Split(output, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			result = append(result, line)
		}
	}
	return result, nil
}

func (b *CommandBackend) Close() error {
	return nil
}

func (b *CommandBackend) run(ctx context.Context, args []string) (string, error) {
	result, err := b.runResult(ctx, args)
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
}

func (b *CommandBackend) runResult(ctx context.Context, args []string) (CommandResult, error) {
	if b.runner == nil {
		return CommandResult{}, fmt.Errorf("%w: command runner", ErrNoBackend)
	}
	result, err := b.runner.RunCommand(ctx, b.command(args))
	if err != nil {
		return CommandResult{}, err
	}
	if result.Truncated {
		return CommandResult{}, errors.New("docker command output exceeded the transport limit")
	}
	if result.ExitCode != nil && *result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = strings.TrimSpace(result.Stdout)
		}
		return CommandResult{}, fmt.Errorf("docker command exited with code %d: %s", *result.ExitCode, message)
	}
	return result, nil
}

func (b *CommandBackend) open(ctx context.Context, command string, width, height uint) (CommandStream, error) {
	if b.opener == nil {
		return nil, fmt.Errorf("%w: streaming command channel", ErrUnsupported)
	}
	return b.opener.OpenCommand(ctx, command, width, height)
}

func (b *CommandBackend) command(args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, b.binary)
	for _, arg := range args {
		parts = append(parts, quoteShell(arg, b.shell))
	}
	return strings.Join(parts, " ")
}

type cliContainer struct {
	ID     string `json:"ID"`
	Names  string `json:"Names"`
	Image  string `json:"Image"`
	State  string `json:"State"`
	Status string `json:"Status"`
	Ports  string `json:"Ports"`
	Labels string `json:"Labels"`
}

type cliImage struct {
	ID           string `json:"ID"`
	Repository   string `json:"Repository"`
	Tag          string `json:"Tag"`
	Size         string `json:"Size"`
	CreatedSince string `json:"CreatedSince"`
}

func decodeJSONLines[T any](output string) ([]T, error) {
	var result []T
	for lineNumber, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var value T
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			return nil, fmt.Errorf("decode docker JSON line %d: %w", lineNumber+1, err)
		}
		result = append(result, value)
	}
	if result == nil {
		return []T{}, nil
	}
	return result, nil
}

func cliLabel(labels, key string) (string, bool) {
	for _, label := range strings.Split(labels, ",") {
		name, value, ok := strings.Cut(label, "=")
		if ok && name == key {
			return value, true
		}
	}
	return "", false
}

func quoteShell(value string, shell Shell) string {
	if shell == ShellPowerShell {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type commandExecSession struct {
	CommandStream
	reader io.ReadCloser
}

func newCommandExecSession(stream CommandStream) *commandExecSession {
	return &commandExecSession{CommandStream: stream, reader: combinedCommandOutput(stream)}
}

func (s *commandExecSession) Read(buffer []byte) (int, error) {
	return s.reader.Read(buffer)
}

func (s *commandExecSession) Close() error {
	return s.reader.Close()
}

func (s *commandExecSession) IsTTY() bool {
	return true
}
