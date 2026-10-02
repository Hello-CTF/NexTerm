package mount

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

type command struct {
	name        string
	args        []string
	stdin       []byte
	interactive bool
}

type commandResult struct {
	stdout string
	stderr string
}

type commandRunner interface {
	LookPath(string) (string, error)
	Run(context.Context, command) (commandResult, error)
}

type commandExitError struct {
	code int
}

func (e *commandExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

type execRunner struct {
	timeout time.Duration
}

func (r execRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (r execRunner) Run(ctx context.Context, input command) (commandResult, error) {
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	if input.interactive {
		return runInteractive(ctx, input)
	}
	return runNative(ctx, input)
}

func runNative(ctx context.Context, input command) (commandResult, error) {
	process := exec.CommandContext(ctx, input.name, input.args...)
	process.Stdin = bytes.NewReader(input.stdin)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = &stderr
	configureCommand(process)
	err := commandContextError(ctx, process.Run())
	return commandResult{stdout: stdout.String(), stderr: stderr.String()}, err
}

func commandContextError(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
