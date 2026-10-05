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
)
