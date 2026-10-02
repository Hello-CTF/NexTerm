package tasks

import (
	"context"
	"errors"
	"time"
)

// State is the lifecycle state of a task. Terminal states are StateSucceeded,
// StateFailed, StateKilled, and StateInterrupted; a task enters exactly one of
// them, exactly once.
type State string

const (
	StateRunning     State = "running"
	StateSucceeded   State = "succeeded"
	StateFailed      State = "failed"
	StateKilled      State = "killed"
	StateInterrupted State = "interrupted"
)

// Terminal reports whether the state is terminal.
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateKilled, StateInterrupted:
		return true
	}
	return false
}

var (
	// ErrNotFound is returned for unknown task IDs and for tasks owned by a
	// different object, so cross-owner probing cannot distinguish the two.
	ErrNotFound = errors.New("task not found")
	// ErrClosed is returned by operations on a closed Manager.
	ErrClosed = errors.New("task manager closed")
	// ErrInvalidOwner is returned when an owner kind or ID is empty.
	ErrInvalidOwner = errors.New("task owner kind and id are required")
	// ErrInvalidCommand is returned when the command path is empty.
	ErrInvalidCommand = errors.New("task command path is required")
	// ErrInvalidOffset is returned for negative output offsets.
	ErrInvalidOffset = errors.New("task output offset is negative")
)

// Owner identifies the object a task belongs to, for example a session or an
// agent run. Ownership is part of every lookup: a task is only visible to the
// exact owner it was started with.
type Owner struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (o Owner) valid() bool { return o.Kind != "" && o.ID != "" }

// Command describes the program to execute. Env entries are extra KEY=VALUE
// pairs appended to the inherited environment. Env is never persisted or
// exposed through Info so credentials do not leak into task records.
type Command struct {
	Path string   `json:"path"`
	Args []string `json:"args,omitempty"`
	Dir  string   `json:"dir,omitempty"`
	Env  []string `json:"-"`
}

// Info is the observable snapshot of a task.
type Info struct {
	ID        string     `json:"id"`
	Owner     Owner      `json:"owner"`
	Command   Command    `json:"command"`
	State     State      `json:"state"`
	Detached  bool       `json:"detached"`
	CreatedAt time.Time  `json:"createdAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	ExitCode  *int       `json:"exitCode,omitempty"`
	Error     string     `json:"error,omitempty"`
	// OutputBytes is the total number of output bytes ever produced.
	OutputBytes int64 `json:"outputBytes"`
	// DroppedBytes is the number of middle bytes elided by bounded retention.
	DroppedBytes int64 `json:"droppedBytes"`
}

// Output is a retained output snapshot: the head of the stream and the most
// recent tail. When Total exceeds the retained bytes, Dropped counts the
// elided middle between Head and Tail; the stream was never re-executed.
type Output struct {
	Head    []byte `json:"head"`
	Tail    []byte `json:"tail"`
	Total   int64  `json:"total"`
	Dropped int64  `json:"dropped"`
}

// Chunk is an incremental output read. Data starts at the logical stream
// Offset; the next read should pass Next. If the requested offset fell into
// the elided middle, the read resumes at the tail and Gap is true. Total is
// the total bytes ever produced at read time.
type Chunk struct {
	Data   []byte `json:"data"`
	Offset int64  `json:"offset"`
	Next   int64  `json:"next"`
	Total  int64  `json:"total"`
	Gap    bool   `json:"gap"`
}

// Result is the outcome of Run. When Detached is true the task persists in
// the background and Info describes a live or already-terminal retained task;
// otherwise the command completed synchronously and no task or spool files
// remain. Output is the retained snapshot at return time.
type Result struct {
	Info     Info   `json:"info"`
	Output   Output `json:"output"`
	Detached bool   `json:"detached"`
}

// Action identifies an operation for the authorization hook.
type Action string

const (
	ActionStart  Action = "start"
	ActionList   Action = "list"
	ActionGet    Action = "get"
	ActionOutput Action = "output"
	ActionKill   Action = "kill"
)

// Hooks customize integration behavior. All hooks are optional.
type Hooks struct {
	// Authorize, when set, runs before every operation with the caller's
	// owner scope. Task is nil for ActionStart and ActionList. A non-nil
	// error aborts the operation. Ownership checks always apply, even
	// without this hook.
	Authorize func(ctx context.Context, action Action, owner Owner, task *Info) error
	// NewID generates stable task IDs; defaults to a ULID generator.
	NewID func() string
	// Now supplies the clock; defaults to time.Now.
	Now func() time.Time
}

// Default retention bounds.
const (
	DefaultHeadBytes   = 64 << 10
	DefaultTailBytes   = 256 << 10
	DefaultMaxRetained = 128
	// maxReadBytes caps a single incremental read.
	maxReadBytes = 1 << 20
)
