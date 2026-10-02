package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const defaultExecTimeout = 30 * time.Second

func (t *Transport) OpenExec(ctx context.Context, command string, options base.ExecOptions) (base.Channel, error) {
	if err := t.check(ctx, options.ExpectedGeneration); err != nil {
		return nil, err
	}
	if options.Timeout < 0 {
		return nil, fmt.Errorf("exec timeout must not be negative")
	}
	opCtx := ctx
	cancel := func() {}
	if options.Timeout > 0 {
		opCtx, cancel = context.WithTimeout(ctx, options.Timeout)
	} else {
		opCtx, cancel = context.WithCancel(ctx)
	}
	cmd := exec.Command(t.shell, shellArguments(command)...)
	cmd.Dir = t.cwd
	cmd.Env = environment(t.env, false, "")
	channel, err := openExecChannel(opCtx, cancel, cmd, t.channelID("exec"), t.generation)
	if err != nil {
		return nil, err
	}
	return t.track(opCtx, channel)
}

func (t *Transport) Exec(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
	started := time.Now()
	if options.Timeout == 0 {
		options.Timeout = defaultExecTimeout
	}
	channel, err := t.OpenExec(ctx, command, options)
	if err != nil {
		return base.ExecResult{Duration: time.Since(started)}, err
	}
	defer channel.Close()
	if err := channel.CloseWrite(); err != nil {
		return base.ExecResult{Duration: time.Since(started)}, err
	}
	limits := options.Limits.Normalized()
	stdout := &limitedBuffer{maximum: limits.Stdout}
	stderr := &limitedBuffer{maximum: limits.Stderr}
	copyErrors := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := io.Copy(stdout, channel)
		copyErrors <- err
	}()
	go func() {
		defer wg.Done()
		_, err := io.Copy(stderr, channel.Stderr())
		copyErrors <- err
	}()
	waitErr := channel.Wait(ctx)
	if waitErr != nil {
		var exitErr *base.ExitError
		if !errors.As(waitErr, &exitErr) {
			_ = channel.Close()
		}
	}
	wg.Wait()
	close(copyErrors)
	result := base.ExecResult{
		Stdout:    strings.ToValidUTF8(stdout.String(), "\uFFFD"),
		Stderr:    strings.ToValidUTF8(stderr.String(), "\uFFFD"),
		Duration:  time.Since(started),
		Truncated: stdout.truncated || stderr.truncated,
	}
	if waitErr != nil {
		var exitErr *base.ExitError
		if errors.As(waitErr, &exitErr) {
			result.ExitCode = &exitErr.Code
			result.Signal = exitErr.Signal
			return result, nil
		}
		return result, waitErr
	}
	code := 0
	result.ExitCode = &code
	for copyErr := range copyErrors {
		if copyErr != nil && !errors.Is(copyErr, os.ErrClosed) {
			return result, fmt.Errorf("read local exec output: %w", copyErr)
		}
	}
	return result, nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	maximum   int64
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	if b.maximum >= 0 {
		remaining := b.maximum - int64(b.buffer.Len())
		if remaining <= 0 {
			b.truncated = b.truncated || len(p) > 0
			return original, nil
		}
		if int64(len(p)) > remaining {
			p = p[:remaining]
			b.truncated = true
		}
	}
	_, _ = b.buffer.Write(p)
	return original, nil
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}
