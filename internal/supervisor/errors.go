package supervisor

import "errors"

var (
	ErrUnavailable   = errors.New("supervisor session unavailable")
	ErrNotFound      = errors.New("supervisor session not found")
	ErrAlreadyExists = errors.New("supervisor session already exists")
	ErrIdentity      = errors.New("supervisor session identity changed")
	ErrExited        = errors.New("supervisor session process exited")
	ErrClosed        = errors.New("supervisor session attachment closed")
	ErrInvalidInput  = errors.New("invalid supervisor session input")
	ErrProtocol      = errors.New("supervisor protocol violation")
	ErrStateMismatch = errors.New("supervisor state mismatch")
	ErrUnsupported   = errors.New("supervisor capability unsupported")
)
