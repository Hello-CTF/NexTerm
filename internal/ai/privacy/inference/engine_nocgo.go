//go:build !cgo

package inference

import "context"

type Engine struct{}

func selfLibraryPath() string { return "" }

func Open(string, string, string) (*Engine, error)             { return nil, ErrUnavailable }
func (*Engine) Detect(context.Context, string) ([]Span, error) { return nil, ErrUnavailable }
func (*Engine) Close() error                                   { return nil }
