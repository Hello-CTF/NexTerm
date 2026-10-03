package subagent

import (
	"context"
	"errors"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/steer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var (
	ErrNestedSpawn            = errors.New("nested subagent spawning is forbidden")
	ErrScopeRequired          = errors.New("an explicit subagent permission scope is required")
	ErrToolUnavailable        = errors.New("a scoped subagent tool is unavailable")
	ErrTaskNotFound           = errors.New("subagent task not found")
	ErrStaleGeneration        = errors.New("subagent task handle has a stale generation")
	ErrTaskFinished           = errors.New("subagent task has already finished")
	ErrTaskExists             = errors.New("subagent task ID is already active")
	ErrRegistryFull           = errors.New("subagent task registry is full")
	ErrTooManyActive          = errors.New("too many active subagent tasks")
	ErrManagerClosed          = errors.New("subagent manager is closed")
	ErrInteractionUnsupported = errors.New("subagent tasks cannot request user interaction")
)

type ModelFactory func(context.Context) (model.BaseChatModel, error)

type ToolFactory func(context.Context, Scope) ([]tool.BaseTool, error)

type Config struct {
	NewModel           ModelFactory
	NewTools           ToolFactory
	Instruction        string
	MaxIterations      int
	MaxRunTime         time.Duration
	MaxTasks           int
	MaxConcurrent      int
	MaxOutputBytes     int
	MaxHistoryMessages int
	MaxHistoryBytes    int
	NewID              func() string
}

type Scope struct {
	AllowedTools []string
}

type Request struct {
	ID      string
	Task    string
	Persona string
	Scope   *Scope
	Timeout time.Duration
}

type Handle struct {
	ID         string
	Generation uint64
}

type Status string

const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

type Result struct {
	Handle           Handle
	Status           Status
	Output           string
	OutputTruncated  bool
	History          []*schema.Message
	HistoryTruncated bool
	Error            string
}

func (r Result) clone() Result {
	r.History = steer.CloneHistory(r.History)
	return r
}
