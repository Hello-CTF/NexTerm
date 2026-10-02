package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type runCommand func(context.Context, ...string) ([]byte, error)

type commandError struct {
	op     string
	stderr string
	err    error
}

func (e *commandError) Error() string {
	if e.stderr == "" {
		return fmt.Sprintf("tmux %s: %v", e.op, e.err)
	}
	return fmt.Sprintf("tmux %s: %v: %s", e.op, e.err, e.stderr)
}

func (e *commandError) Unwrap() error { return e.err }

func (b *Backend) execute(ctx context.Context, args ...string) ([]byte, error) {
	commandArgs := []string{"-f", os.DevNull, "-S", b.socketPath}
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, b.binary, commandArgs...)
	cmd.Env = cleanEnvironment(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: tmux executable: %v", ErrUnavailable, err)
		}
		message := strings.TrimSpace(stderr.String())
		if len(message) > 4096 {
			message = message[:4096]
		}
		return nil, &commandError{op: args[0], stderr: message, err: err}
	}
	return stdout.Bytes(), nil
}

func cleanEnvironment(environment []string) []string {
	cleaned := make([]string, 0, len(environment)+1)
	localeSet := false
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "TMUX", "TMUX_PANE":
			continue
		case "LC_ALL":
			localeSet = true
			entry = "LC_ALL=C"
		}
		cleaned = append(cleaned, entry)
	}
	if !localeSet {
		cleaned = append(cleaned, "LC_ALL=C")
	}
	return cleaned
}

func (b *Backend) run(ctx context.Context, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, b.commandTimeout)
	defer cancel()
	return b.runner(commandCtx, args...)
}
