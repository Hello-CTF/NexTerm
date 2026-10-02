package base

import (
	"context"
	"io"
)

type PTYOptions struct {
	Cols               uint32
	Rows               uint32
	Term               string
	Modes              map[byte]uint32
	ExpectedGeneration uint64
}

type Channel interface {
	io.ReadWriteCloser
	Stderr() io.Reader
	Resize(ctx context.Context, cols, rows uint32) error
	Wait(ctx context.Context) error
	CloseWrite() error
	ID() string
	Generation() uint64
}
