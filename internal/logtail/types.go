package logtail

import (
	"context"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	DefaultConcurrency = 4
	MaxConcurrency     = 32
	MaxSources         = 64
	MaxSourceValueSize = 4096
	MaxEventLineSize   = 32 << 10
)

var (
	ErrInvalid      = errors.New("invalid log tail")
	ErrConflict     = errors.New("log tail already active")
	ErrForbidden    = errors.New("log tail forbidden")
	ErrClosed       = errors.New("log tail service closed")
	ErrUnsupported  = errors.New("log tail streaming unsupported")
	ErrPendingLimit = errors.New("too many pending log tail cancellations")
)

type SourceKind string

const (
	SourceFile    SourceKind = "file"
	SourceJournal SourceKind = "journal"
	SourceDocker  SourceKind = "docker"
)

type Source struct {
	SourceID    string     `json:"sourceId"`
	SessionID   string     `json:"sessionId"`
	SessionName string     `json:"sessionName,omitempty"`
	Kind        SourceKind `json:"kind"`
	Value       string     `json:"value"`
}

type Request struct {
	RunID         string   `json:"runId"`
	Sources       []Source `json:"sources"`
	MaxConcurrent int      `json:"maxConcurrent,omitempty"`
}

type StartResult struct {
	RunID string `json:"runId"`
}

type CancelResult struct {
	RunID    string `json:"runId"`
	Canceled bool   `json:"canceled"`
}

type SourceStatus string

const (
	StatusQueued   SourceStatus = "queued"
	StatusStarting SourceStatus = "starting"
	StatusRunning  SourceStatus = "running"
	StatusExited   SourceStatus = "exited"
	StatusFailed   SourceStatus = "failed"
	StatusCanceled SourceStatus = "canceled"
)

type EventKind string

const (
	EventStatus EventKind = "status"
	EventLine   EventKind = "line"
	EventDone   EventKind = "done"
)

type Event struct {
	RunID       string       `json:"runId"`
	Sequence    uint64       `json:"sequence"`
	Kind        EventKind    `json:"type"`
	SourceID    string       `json:"sourceId,omitempty"`
	SessionID   string       `json:"sessionId,omitempty"`
	SessionName string       `json:"sessionName,omitempty"`
	Status      SourceStatus `json:"status,omitempty"`
	Text        string       `json:"text,omitempty"`
	Stderr      bool         `json:"stderr,omitempty"`
	Error       string       `json:"error,omitempty"`
	TimeMS      int64        `json:"timeMs"`
}

type Target struct {
	SessionID   string
	SessionName string
	Transport   base.Transport
	Generation  uint64
}

type Resolver interface {
	Resolve(context.Context, string) (Target, error)
}

type ResolverFunc func(context.Context, string) (Target, error)

func (f ResolverFunc) Resolve(ctx context.Context, sessionID string) (Target, error) {
	return f(ctx, sessionID)
}

type Sink interface {
	Send(context.Context, Event) error
	Close() error
}

type Config struct {
	Resolver Resolver
	Now      func() time.Time
}
