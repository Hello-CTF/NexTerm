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
	ErrOutputLimit       = errors.New("invalid output limit")
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

func ExitCode(err error) (int, bool) {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code, true
	}
	return 0, false
}
