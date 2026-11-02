package execbatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// streamCopyBuffer bounds the chunks forwarded from a transport stream into
// the progress tracker.
const streamCopyBuffer = 32 << 10

// TargetStreamOptions tunes one streamed target execution.
type TargetStreamOptions struct {
	Timeout time.Duration
	Now     func() time.Time
	// OutputLimit caps the captured full output for the final Result;
	// DefaultOutputLimit when unset. The progress retention bounds apply
	// independently of this limit.
	OutputLimit int64
	// Interactor, when set, turns script directives in stdout into events and
	// prompts. Nil keeps the output untouched and stdin closed immediately.
	Interactor Interactor
	// OnAnswered observes each answered prompt; it never receives a secret value.
	OnAnswered func(AnswerRecord)
}

// targetStreamOutcome carries the execution side of a streamed target; the
// status mapping in streamTargetResult stays the single place that turns it
// into a Result.
type targetStreamOutcome struct {
	stdoutTruncated bool
	stderrTruncated bool
	err             error
}

// ExecuteTargetStream runs one target's command and streams its output into
// the tracker: Start, per-chunk bounded retention through WriteStdout and
// WriteStderr, then Finish with the mapped result. When the target's
// transport implements base.ExecStreamTransport the output is consumed
// live; otherwise the one-shot Exec result is forwarded as a single chunk
// per stream and execution is not live.
//
// The returned Result carries status, exit code, signal, error, duration,
// and truncation; the command output itself is not duplicated — the bounded
// head/tail lives in the tracker's snapshots. Cancellation follows the
// service contract: a canceled context finishes the target with
// StatusCanceled, a deadline with StatusTimeout.
func ExecuteTargetStream(ctx context.Context, tracker *ProgressTracker, target Target, command string, options TargetStreamOptions) (Result, error) {
	return executeTargetStream(ctx, tracker, target, command, options, nil)
}

// ExecuteTargetStreamCaptured is ExecuteTargetStream plus a write-time
// capped capture of the full output for the final Result, mirroring the
// service's one-shot output contract: Stdout and Stderr hold the first
// OutputLimit bytes of each stream (rune-safe), and Truncated reports the
// capture cap rather than the progress retention.
func ExecuteTargetStreamCaptured(ctx context.Context, tracker *ProgressTracker, target Target, command string, options TargetStreamOptions) (Result, error) {
	limit := options.OutputLimit
	if limit <= 0 {
		limit = DefaultOutputLimit
	}
	return executeTargetStream(ctx, tracker, target, command, options, newStreamCapture(limit))
}

// streamCapture collects the full output for the final Result under a
// write-time cap so an unbounded stream cannot grow memory.
type streamCapture struct {
	stdout streamLimitedBuffer
	stderr streamLimitedBuffer
}

type streamLimitedBuffer struct {
	buffer    []byte
	limit     int64
	truncated bool
}

func newStreamCapture(limit int64) *streamCapture {
	return &streamCapture{
		stdout: streamLimitedBuffer{limit: limit},
		stderr: streamLimitedBuffer{limit: limit},
	}
}

func (b *streamLimitedBuffer) Write(p []byte) {
	if b.truncated || int64(len(b.buffer)) >= b.limit {
		b.truncated = true
		return
	}
	room := b.limit - int64(len(b.buffer))
	if int64(len(p)) > room {
		b.buffer = append(b.buffer, p[:room]...)
		b.truncated = true
		return
	}
	b.buffer = append(b.buffer, p...)
}

func executeTargetStream(ctx context.Context, tracker *ProgressTracker, target Target, command string, options TargetStreamOptions, capture *streamCapture) (Result, error) {
	if tracker == nil {
		return Result{}, fmt.Errorf("%w: nil progress tracker", ErrInvalid)
	}
	if command == "" {
		return Result{}, fmt.Errorf("%w: empty command", ErrInvalid)
	}
	if target.Transport == nil {
		return Result{}, fmt.Errorf("%w: %s has no transport", ErrUnsupported, target.SessionID)
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	started := now()
	if err := tracker.Start(target.SessionID, started); err != nil {
		return Result{}, err
	}
	result := streamTargetResult(ctx, tracker, target, command, timeout, started, capture, options)
	result.finishedAt = now()
	result.DurationMS = result.finishedAt.Sub(started).Milliseconds()
	if err := tracker.Finish(target.SessionID, result, result.finishedAt); err != nil {
		return Result{}, err
	}
	return result, nil
}

func streamTargetResult(ctx context.Context, tracker *ProgressTracker, target Target, command string, timeout time.Duration, started time.Time, capture *streamCapture, options TargetStreamOptions) Result {
	result := Result{
		SessionID: target.SessionID, SessionName: target.SessionName, AssetID: target.AssetID,
		startedAt: started, environment: target.Environment,
	}
	var (
		opCtx    context.Context
		opCancel func()
		deadline *pausableDeadline
	)
	if options.Interactor != nil && execStdinSupported(target.Transport) {
		// The exec timeout stops running while the operator is asked, so a slow
		// human is not reported as a command timeout.
		cancelCause, cancel := context.WithCancelCause(ctx)
		opCtx = cancelCause
		deadline = newPausableDeadline(timeout, func() { cancel(context.DeadlineExceeded) })
		opCancel = func() { deadline.Stop(); cancel(context.Canceled) }
	} else {
		var cancel context.CancelFunc
		opCtx, cancel = context.WithTimeout(ctx, timeout)
		opCancel = cancel
	}
	defer opCancel()

	var interact *interactState
	if options.Interactor != nil {
		now := options.Now
		if now == nil {
			now = time.Now
		}
		interact = &interactState{
			interactor: options.Interactor, target: target, deadline: deadline,
			answered: options.OnAnswered, now: now, stdinWhy: stdinUnavailableReason(target.Transport),
		}
	}

	var outcome targetStreamOutcome
	if stream, ok := target.Transport.(base.ExecStreamTransport); ok {
		outcome = streamTargetOutput(opCtx, tracker, target, command, timeout, stream, capture, interact)
	} else {
		outcome = forwardOneShotOutput(opCtx, tracker, target, command, timeout, capture, interact)
	}
	if capture != nil {
		result.Stdout, _ = capOutput(string(capture.stdout.buffer), capture.stdout.limit)
		result.Stderr, _ = capOutput(string(capture.stderr.buffer), capture.stderr.limit)
		result.Truncated = outcome.stdoutTruncated || outcome.stderrTruncated ||
			capture.stdout.truncated || capture.stderr.truncated
		return resultWithMappedStatus(&result, opCtx, outcome)
	}
	snapshot := targetStreamSnapshot(tracker, target.SessionID)
	result.Truncated = outcome.stdoutTruncated || outcome.stderrTruncated ||
		snapshot.Stdout.Truncated || snapshot.Stderr.Truncated
	return resultWithMappedStatus(&result, opCtx, outcome)
}

func resultWithMappedStatus(result *Result, opCtx context.Context, outcome targetStreamOutcome) Result {
	switch opErr := opCtx.Err(); {
	case opErr != nil:
		reason := context.Cause(opCtx)
		result.Error = reason.Error()
		if errors.Is(reason, context.DeadlineExceeded) {
			result.Status = StatusTimeout
		} else {
			result.Status = StatusCanceled
		}
	case outcome.err == nil:
		result.Status = StatusSuccess
		code := 0
		result.ExitCode = &code
	default:
		mapTargetStreamError(result, outcome.err)
	}
	return *result
}

func mapTargetStreamError(result *Result, err error) {
	var exitErr *base.ExitError
	switch {
	case errors.As(err, &exitErr):
		code, _ := base.ExitCode(err)
		result.Status = StatusExitError
		result.ExitCode = &code
		result.Signal = exitErr.Signal
	case errors.Is(err, context.DeadlineExceeded):
		result.Status = StatusTimeout
		result.Error = err.Error()
	case errors.Is(err, context.Canceled):
		result.Status = StatusCanceled
		result.Error = err.Error()
	default:
		result.Status = StatusTransportError
		result.Error = err.Error()
	}
}

// streamTargetOutput consumes the live channel until both streams end and
// the command exits, forwarding every chunk into the tracker. Batch
// commands carry no stdin input, so the write side closes immediately and
// EOF-driven commands observe the same EOF as one-shot Exec. Only a command
// that opted into directives (an Interactor is set) over a stdin-capable transport
// (interact.deadline set) keeps stdin open for operator answers. On a normal
// exit or a remote ExitError the copies drain to EOF before the channel is
// closed; an early close is only used to unblock the copies after a
// cancellation or transport failure, and it never touches the shared
// transport or any other exec channel.
func streamTargetOutput(ctx context.Context, tracker *ProgressTracker, target Target, command string, timeout time.Duration, stream base.ExecStreamTransport, capture *streamCapture, interact *interactState) targetStreamOutcome {
	openTimeout := timeout
	keepStdin := interact != nil && interact.deadline != nil
	if keepStdin {
		// The pausable deadline in ctx replaces the transport's own timeout.
		openTimeout = 0
	}
	channel, err := stream.OpenExec(ctx, command, base.ExecOptions{Timeout: openTimeout, ExpectedGeneration: target.Generation})
	if err != nil {
		return targetStreamOutcome{err: err}
	}
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	var (
		fatal   error
		fatalMu sync.Mutex
	)
	setFatal := func(err error) {
		fatalMu.Lock()
		if fatal == nil {
			fatal = err
		}
		fatalMu.Unlock()
		cancelWait()
	}
	if keepStdin {
		var writeMu sync.Mutex
		interact.stdin = func(line string) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			_, err := channel.Write([]byte(line))
			return err
		}
	} else if err := channel.CloseWrite(); err != nil {
		_ = channel.Close()
		return targetStreamOutcome{err: fmt.Errorf("close exec stdin: %w", err)}
	}
	var cancelAsk context.CancelFunc = func() {}
	if interact != nil {
		interact.askCtx, cancelAsk = context.WithCancel(waitCtx)
		defer cancelAsk()
	}
	var stdoutErr, stderrErr error
	var copies sync.WaitGroup
	copies.Add(2)
	go func() {
		defer copies.Done()
		stderrErr = copyStreamToTracker(tracker, target.SessionID, channel.Stderr(), false, capture)
	}()
	go func() {
		defer copies.Done()
		if interact == nil {
			stdoutErr = copyStreamToTracker(tracker, target.SessionID, channel, true, capture)
			return
		}
		stdoutErr = copyDirectiveStream(tracker, target.SessionID, channel, capture, interact)
		if stdoutErr != nil && interact.askCtx.Err() == nil {
			// A handler failure (input timeout, bad answer, stdin write error) ends the target.
			setFatal(stdoutErr)
		}
	}()
	waitErr := channel.Wait(waitCtx)
	// The script is gone or being torn down: nobody can answer a pending prompt anymore.
	cancelAsk()
	var exitErr *base.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		// Cancellation or transport failure: close early so the copies
		// unblock; the context error below still decides the status.
		_ = channel.Close()
	}
	copies.Wait()
	waitErr = channel.Wait(ctx)
	_ = channel.Close()

	fatalMu.Lock()
	fatalErr := fatal
	fatalMu.Unlock()
	switch {
	case ctx.Err() != nil:
		return targetStreamOutcome{err: ctx.Err()}
	case fatalErr != nil:
		return targetStreamOutcome{err: fatalErr}
	case stdoutErr != nil:
		return targetStreamOutcome{err: fmt.Errorf("read exec stdout: %w", stdoutErr)}
	case stderrErr != nil:
		return targetStreamOutcome{err: fmt.Errorf("read exec stderr: %w", stderrErr)}
	default:
		return targetStreamOutcome{err: waitErr}
	}
}

// forwardOneShotOutput runs the request/response fallback for transports
// without streaming support and forwards the full output through the
// tracker's bounded retention as one chunk per stream.
func forwardOneShotOutput(ctx context.Context, tracker *ProgressTracker, target Target, command string, timeout time.Duration, capture *streamCapture, interact *interactState) targetStreamOutcome {
	options := base.ExecOptions{Timeout: timeout, ExpectedGeneration: target.Generation}
	if capture != nil {
		options.Limits = base.OutputLimits{Stdout: capture.stdout.limit, Stderr: capture.stderr.limit}
	}
	execResult, execErr := target.Transport.Exec(ctx, command, options)
	if execResult.Stdout != "" && interact != nil {
		// No live channel and no stdin: directives are still hidden and shown,
		// input requests are reported as unsupported.
		interact.askCtx = ctx
		filter := newDirectiveFilter(func(chunk []byte) error {
			return writeStdoutChunk(tracker, target.SessionID, chunk, capture)
		}, interact.handle, interact.malformed)
		_ = filter.Write([]byte(execResult.Stdout))
		_ = filter.Close()
		interact.flushProgress()
	} else if execResult.Stdout != "" {
		_ = tracker.WriteStdout(target.SessionID, []byte(execResult.Stdout))
		if capture != nil {
			capture.stdout.Write([]byte(execResult.Stdout))
		}
	}
	if execResult.Stderr != "" {
		_ = tracker.WriteStderr(target.SessionID, []byte(execResult.Stderr))
		if capture != nil {
			capture.stderr.Write([]byte(execResult.Stderr))
		}
	}
	outcome := targetStreamOutcome{stdoutTruncated: execResult.Truncated, stderrTruncated: execResult.Truncated, err: execErr}
	switch {
	case execErr == nil && execResult.ExitCode == nil:
		outcome.err = base.ErrExitStatusMissing
	case execErr == nil && *execResult.ExitCode != 0:
		outcome.err = &base.ExitError{Code: *execResult.ExitCode, Signal: execResult.Signal}
	}
	return outcome
}

func writeStdoutChunk(tracker *ProgressTracker, sessionID string, chunk []byte, capture *streamCapture) error {
	err := tracker.WriteStdout(sessionID, chunk)
	if capture != nil {
		capture.stdout.Write(chunk)
	}
	return err
}

// copyDirectiveStream copies stdout like copyStreamToTracker but hides
// directive lines and routes them to the interact state. It blocks while a
// prompt waits for the operator, which also back-pressures the script.
func copyDirectiveStream(tracker *ProgressTracker, sessionID string, reader io.Reader, capture *streamCapture, interact *interactState) error {
	filter := newDirectiveFilter(func(chunk []byte) error {
		return writeStdoutChunk(tracker, sessionID, chunk, capture)
	}, interact.handle, interact.malformed)
	buffer := make([]byte, streamCopyBuffer)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			if writeErr := filter.Write(buffer[:count]); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			closeErr := filter.Close()
			interact.flushProgress()
			if closeErr != nil {
				return closeErr
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func copyStreamToTracker(tracker *ProgressTracker, sessionID string, reader io.Reader, stdout bool, capture *streamCapture) error {
	buffer := make([]byte, streamCopyBuffer)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			chunk := make([]byte, count)
			copy(chunk, buffer[:count])
			var writeErr error
			if stdout {
				writeErr = tracker.WriteStdout(sessionID, chunk)
				if capture != nil {
					capture.stdout.Write(chunk)
				}
			} else {
				writeErr = tracker.WriteStderr(sessionID, chunk)
				if capture != nil {
					capture.stderr.Write(chunk)
				}
			}
			if writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func targetStreamSnapshot(tracker *ProgressTracker, sessionID string) TargetOutputSnapshot {
	_, targets := tracker.Snapshot()
	for _, target := range targets {
		if target.SessionID == sessionID {
			return target.Output
		}
	}
	return TargetOutputSnapshot{}
}
