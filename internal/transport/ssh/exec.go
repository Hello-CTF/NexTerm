package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const defaultExecTimeout = 30 * time.Second

func (c *Client) Exec(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
	started := time.Now()
	if options.Timeout == 0 {
		options.Timeout = defaultExecTimeout
	}
	limits := options.Limits.Normalized()
	channel, err := c.OpenExec(ctx, command, options)
	if err != nil {
		return base.ExecResult{Duration: time.Since(started)}, err
	}
	defer channel.Close()

	stdout := &limitedBuffer{maximum: limits.Stdout}
	stderr := &limitedBuffer{maximum: limits.Stderr}
	var wg sync.WaitGroup
	copyErrors := make(chan error, 2)
	copyStream := func(reader io.Reader, writer io.Writer) {
		defer wg.Done()
		_, err := io.Copy(writer, reader)
		copyErrors <- err
	}
	wg.Add(2)
	go copyStream(channel, stdout)
	go copyStream(channel.Stderr(), stderr)
	waitErr := channel.Wait(ctx)
	channel.Close()
	wg.Wait()
	close(copyErrors)

	result := base.ExecResult{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Duration:  time.Since(started),
		Truncated: stdout.truncated || stderr.truncated,
	}
	if waitErr != nil {
		if code, ok := base.ExitCode(waitErr); ok {
			result.ExitCode = &code
			var exitErr *base.ExitError
			if errors.As(waitErr, &exitErr) {
				result.Signal = exitErr.Signal
			}
			return result, nil
		}
		return result, waitErr
	}
	code := 0
	result.ExitCode = &code
	for copyErr := range copyErrors {
		if copyErr != nil {
			return result, fmt.Errorf("read SSH exec output: %w", copyErr)
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
