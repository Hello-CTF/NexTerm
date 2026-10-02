package base

import (
	"context"
	"net"
	"time"
)

const (
	DefaultStdoutLimit int64 = 8 << 20
	DefaultStderrLimit int64 = 2 << 20
)

type OutputLimits struct {
	Stdout int64
	Stderr int64
}

func (l OutputLimits) Normalized() OutputLimits {
	if l.Stdout == 0 {
		l.Stdout = DefaultStdoutLimit
	}
	if l.Stderr == 0 {
		l.Stderr = DefaultStderrLimit
	}
	return l
}

type ExecOptions struct {
	Timeout            time.Duration
	Limits             OutputLimits
	ExpectedGeneration uint64
}

type ExecResult struct {
	Stdout    string        `json:"stdout"`
	Stderr    string        `json:"stderr"`
	ExitCode  *int          `json:"exitCode"`
	Signal    string        `json:"signal,omitempty"`
	Duration  time.Duration `json:"duration"`
	Truncated bool          `json:"truncated"`
}

type Transport interface {
	Kind() string
	Generation() uint64
	Exec(context.Context, string, ExecOptions) (ExecResult, error)
	Ping(context.Context) (time.Duration, error)
	IsAlive() bool
	Close() error
}

type PTYTransport interface {
	OpenPTY(context.Context, PTYOptions) (Channel, error)
}

type ExecStreamTransport interface {
	OpenExec(context.Context, string, ExecOptions) (Channel, error)
}

type FileTransport interface {
	FileSystem(context.Context) (FileSystem, error)
}

type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}
