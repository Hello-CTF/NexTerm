//go:build !((linux || darwin || windows) && (amd64 || arm64) && (cgo || !(android || ios)))

package inference

import "context"

// ONNX Runtime ships only amd64 and arm64 builds, and purego needs cgo on
// Android and iOS, so other targets report the runtime as unavailable.

type Engine struct{}

func selfLibraryPath() string { return "" }

func CheckPlatform() error { return ErrUnavailable }

func Open(string, string, string) (*Engine, error)             { return nil, ErrUnavailable }
func (*Engine) Detect(context.Context, string) ([]Span, error) { return nil, ErrUnavailable }
func (*Engine) Close() error                                   { return nil }

func LoadRuntime(string) (string, error) { return "", ErrUnavailable }
