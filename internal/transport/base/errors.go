package base

import (
	"errors"
	"fmt"
)

var (
	ErrUnsupported       = errors.New("transport capability unsupported")
	ErrDisconnected      = errors.New("transport disconnected")
	ErrClosed            = errors.New("transport closed")
	ErrStaleGeneration   = errors.New("stale transport generation")
	ErrOutputMode        = errors.New("transport output mode already selected")
	ErrExitStatusMissing = errors.New("remote command exit status missing")
)

type ExitError struct {
	Code   int
	Signal string
}

func (e *ExitError) Error() string {
	if e.Signal != "" {
		return fmt.Sprintf("remote command terminated by signal %s", e.Signal)
	}
	return fmt.Sprintf("remote command exited with status %d", e.Code)
}

// ErrWriteOutcomeUnknown marks a write the caller stopped waiting for while a mutating request was still unanswered.
var ErrWriteOutcomeUnknown = errors.New("write outcome unknown")

// WriteOutcomeUnknownError reports a write that was abandoned during a mutating step: the remote side may still apply
// it, so the file may or may not have been written or replaced. It deliberately does not wrap Reason, the context
// error that ended the wait: the write was not cancelled, only no longer awaited.
type WriteOutcomeUnknownError struct {
	Path   string
	Reason error
}

func (e *WriteOutcomeUnknownError) Error() string {
	return fmt.Sprintf("write to %s has an unknown outcome: stopped waiting (%v) while the remote side had not answered a write step; the file may or may not have been written or replaced, so read it before retrying", e.Path, e.Reason)
}

func (e *WriteOutcomeUnknownError) Unwrap() error { return ErrWriteOutcomeUnknown }

func ExitCode(err error) (int, bool) {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code, true
	}
	return 0, false
}
