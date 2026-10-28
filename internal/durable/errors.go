package durable

import "errors"

var (
	ErrUnavailable   = errors.New("durable terminal unavailable")
	ErrNotFound      = errors.New("durable terminal not found")
	ErrNotOwned      = errors.New("durable session is not owned by this backend")
	ErrAlreadyExists = errors.New("durable terminal already exists")
	ErrIdentity      = errors.New("durable terminal identity changed")
	ErrExited        = errors.New("durable terminal process exited")
	ErrClosed        = errors.New("durable terminal attachment closed")
	ErrInvalidInput  = errors.New("invalid durable terminal input")
	// ErrGenerationMismatch reports that the helper restarted since the
	// terminal was last attached; the terminal's process did not survive it.
	ErrGenerationMismatch = errors.New("durable terminal helper restarted")
	// ErrLimit reports a create the helper refused at its session-count or
	// create-rate limit.
	ErrLimit = errors.New("durable terminal limit reached")
	// ErrSeenUnsupported reports that the helper cannot share a seen attention
	// state, now or later. Transient failures stay ErrUnavailable.
	ErrSeenUnsupported = errors.New("durable terminal does not share seen attention states")
)
