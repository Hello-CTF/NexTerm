package tasks

import (
	"context"
	"errors"
	"time"
)

type State string

const (
	StateRunning     State = "running"
	StateSucceeded   State = "succeeded"
	StateFailed      State = "failed"
	StateKilled      State = "killed"
	StateInterrupted State = "interrupted"
)

func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateKilled, StateInterrupted:
		return true
	}
	return false
}

var (
	ErrNotFound       = errors.New("task not found")
	ErrClosed         = errors.New("task manager closed")
	ErrInvalidOwner   = errors.New("task owner kind and id are required")
	ErrInvalidCommand = errors.New("task command path is required")
	ErrInvalidOffset  = errors.New("task output offset is negative")
)

type Owner struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (o Owner) valid() bool { return o.Kind != "" && o.ID != "" }

type Command struct {
	Path string   `json:"path"`
	Args []string `json:"args,omitempty"`
	Dir  string   `json:"dir,omitempty"`
	Env  []string `json:"-"`
}

type Info struct {
	ID           string     `json:"id"`
	Owner        Owner      `json:"owner"`
	Command      Command    `json:"command"`
	State        State      `json:"state"`
	Detached     bool       `json:"detached"`
	CreatedAt    time.Time  `json:"createdAt"`
	EndedAt      *time.Time `json:"endedAt,omitempty"`
	ExitCode     *int       `json:"exitCode,omitempty"`
	Error        string     `json:"error,omitempty"`
	OutputBytes  int64      `json:"outputBytes"`
	DroppedBytes int64      `json:"droppedBytes"`
	PersistError string     `json:"persistError,omitempty"`
}

type Output struct {
	Head    []byte `json:"head"`
	Tail    []byte `json:"tail"`
	Total   int64  `json:"total"`
	Dropped int64  `json:"dropped"`
}

type Chunk struct {
	Data   []byte `json:"data"`
	Offset int64  `json:"offset"`
	Next   int64  `json:"next"`
	Total  int64  `json:"total"`
	Gap    bool   `json:"gap"`
}

type Result struct {
	Info     Info   `json:"info"`
	Output   Output `json:"output"`
	Detached bool   `json:"detached"`
}

type Action string

const (
	ActionStart  Action = "start"
	ActionList   Action = "list"
	ActionGet    Action = "get"
	ActionOutput Action = "output"
	ActionKill   Action = "kill"
)

type Hooks struct {
	Authorize func(ctx context.Context, action Action, owner Owner, task *Info) error
	NewID     func() string
	Now       func() time.Time
}

const (
	DefaultHeadBytes   = 64 << 10
	DefaultTailBytes   = 256 << 10
	DefaultMaxRetained = 128
	maxReadBytes       = 1 << 20
)
