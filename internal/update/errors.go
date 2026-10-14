package update

import "fmt"

type UserErrorKind uint8

const (
	UserErrorBadParam UserErrorKind = iota + 1
	UserErrorNotFound
	UserErrorIO
)

type UserError struct {
	Kind UserErrorKind
	Err  error
}

func (e *UserError) Error() string {
	return e.Err.Error()
}

func (e *UserError) Unwrap() error {
	return e.Err
}

func userErrorf(kind UserErrorKind, format string, args ...any) error {
	return &UserError{Kind: kind, Err: fmt.Errorf(format, args...)}
}
