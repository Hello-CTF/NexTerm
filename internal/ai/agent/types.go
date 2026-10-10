package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	aicontext "github.com/Hello-CTF/NexTerm/internal/ai/context"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/steer"
	"github.com/Hello-CTF/NexTerm/internal/ai/subagent"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ai/usage"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
)

var (
	ErrJobNotFound         = errors.New("AI 任务不存在或已结束")
	ErrConfirmationStale   = errors.New("确认已过期、重复或不属于当前工具调用")
	ErrInvalidConfirmation = errors.New("确认必须包含 callId、nonce 和有效 decision")
	ErrSteerQueueFull      = errors.New("AI 补充指令队列已满，请稍后再试")
	errRunPaused           = errors.New("AI 任务等待用户交互")
)

type ModelFactory func(context.Context) (model.BaseChatModel, uint64, error)

type ProfileModelFactory func(context.Context, string) (model.BaseChatModel, uint64, error)

type baseChatModelClient interface {
	BaseChatModel(context.Context) (model.BaseChatModel, uint64, error)
}

func activeProfileModel(manager *profiles.Manager) ModelFactory {
	return func(ctx context.Context) (model.BaseChatModel, uint64, error) {
		if _, ok := manager.ActiveProfile(); !ok {
			return nil, 0, errors.New("未配置活动 AI 模型")
		}
		client, err := manager.ActiveClient()
		if err != nil {
			return nil, 0, err
		}
		if adapter, ok := any(client).(baseChatModelClient); ok {
			return adapter.BaseChatModel(ctx)
		}
		return nil, 0, errors.New("当前 provider 未提供 Eino ChatModel 适配")
	}
}

type ConversationStore interface {
	ConvCreate(context.Context, string, any) (store.ConversationRow, error)
	ConvGet(context.Context, string) (store.ConversationRow, error)
	ConvRename(context.Context, string, string) error
	MsgInsert(context.Context, string, string, any, *int64, *int64) error
	MsgList(context.Context, string) ([]store.MessageRow, error)
}

type Config struct {
	Model                 ModelFactory
	ModelForProfile       ProfileModelFactory
	Profiles              *profiles.Manager
	Permissions           *guard.Manager
	Grants                *guard.Grants
	Tools                 *tools.Registry
	Context               *aicontext.Builder
	Store                 ConversationStore
	Runs                  RunStore
	Permission            func(context.Context) (guard.Config, error)
	Checkpoints           adk.CheckPointStore
	HITL                  *hitl.Manager
	HITLTTL               time.Duration
	HITLTerminalRetention time.Duration
	Subagents             *tools.SubagentConfig
	NewID                 func() string
	MaxTurns              int
	MaxImages             int
	MaxImageBytes         int

	MaxPendingSteers int

	Memory      *memory.Store
	MemoryScope memory.Scope

	// ServerMode 标记 nexterm-server 装配(桌面端为 false): 设备授权与授权规则
	// 是全局设置, 多用户模式下仅超管可变更。
	ServerMode bool
}

type ChatArgs struct {
	ConversationID string      `json:"conversationId,omitempty"`
	ChannelID      string      `json:"-"`
	Scope          tools.Scope `json:"scope"`
	Message        string      `json:"message"`
	Selection      string      `json:"selection,omitempty"`
	Images         []string    `json:"images,omitempty"`
	Refs           []RefArg    `json:"refs,omitempty"`
	PlanMode       bool        `json:"planMode,omitempty"`
	Source         string      `json:"-"`
	ModelProfileID string      `json:"-"`
}

type RefArg struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Label     string `json:"label"`
	SessionID string `json:"sessionId,omitempty"`
	Path      string `json:"path,omitempty"`
}

type StartResponse struct {
	JobID          string `json:"jobId"`
	ConversationID string `json:"conversationId"`
}

type Confirmation struct {
	JobID    string `json:"jobId"`
	CallID   string `json:"callId"`
	Nonce    string `json:"nonce"`
	Decision string `json:"decision"`
}

type Answer struct {
	JobID  string `json:"jobId"`
	CallID string `json:"callId"`
	Nonce  string `json:"nonce"`
	Text   string `json:"text"`
}

type queuedEmit struct {
	turn  uint64
	event Event
}

type cancelSync struct {
	started      chan struct{}
	recorded     chan struct{}
	startedOnce  sync.Once
	recordedOnce sync.Once
}

func newCancelSync() *cancelSync {
	return &cancelSync{started: make(chan struct{}), recorded: make(chan struct{})}
}

func (s *cancelSync) markStarted() {
	if s == nil {
		return
	}
	s.startedOnce.Do(func() { close(s.started) })
}

func (s *cancelSync) markRecorded() {
	if s == nil {
		return
	}
	s.recordedOnce.Do(func() { close(s.recorded) })
}

func (s *cancelSync) waitRecorded() {
	if s == nil {
		return
	}
	select {
	case <-s.started:
		<-s.recorded
	default:
	}
}

type job struct {
	id          string
	args        ChatArgs
	ctx         context.Context
	cancel      context.CancelFunc
	deliveryCtx context.Context
	forceCancel context.CancelFunc
	stream      Stream
	memory      *guard.Memory
	eino        *einoRuntime
	cancelFn    adk.AgentCancelFunc
	cancelSync  *cancelSync

	steer *steer.Queue

	emitMu         sync.Mutex
	emitTurn       uint64
	pendingEmits   []queuedEmit
	assistantTurn  uint64
	eventMu        sync.Mutex
	finished       bool
	finalOnce      sync.Once
	completeOnce   sync.Once
	pendingMu      sync.Mutex
	resumeIterator *adk.AsyncIterator[*adk.AgentEvent]
	running        bool
	subagents      *subagent.Manager
	completed      bool
}

func (j *job) emit(ctx context.Context, event Event) error {
	j.eventMu.Lock()
	defer j.eventMu.Unlock()
	if j.finished {
		return errors.New("AI job already finished")
	}
	return j.stream.Send(ctx, event)
}

func (j *job) queueTurnEmits(events ...Event) {
	j.emitMu.Lock()
	j.emitTurn++
	j.queueEmitsLocked(j.emitTurn, events)
	j.emitMu.Unlock()
}

func (j *job) queueEmits(events ...Event) {
	j.emitMu.Lock()
	j.queueEmitsLocked(j.emitTurn, events)
	j.emitMu.Unlock()
}

func (j *job) queueEmitsLocked(turn uint64, events []Event) {
	for _, event := range events {
		j.pendingEmits = append(j.pendingEmits, queuedEmit{turn: turn, event: event})
	}
}

func (j *job) drainEmitsUpTo(turn uint64) []Event {
	j.emitMu.Lock()
	defer j.emitMu.Unlock()
	index := 0
	for index < len(j.pendingEmits) && j.pendingEmits[index].turn <= turn {
		index++
	}
	if index == 0 {
		return nil
	}
	events := make([]Event, 0, index)
	for _, queued := range j.pendingEmits[:index] {
		events = append(events, queued.event)
	}
	j.pendingEmits = append([]queuedEmit(nil), j.pendingEmits[index:]...)
	return events
}

func (j *job) drainAllEmits() []Event {
	j.emitMu.Lock()
	defer j.emitMu.Unlock()
	if len(j.pendingEmits) == 0 {
		return nil
	}
	events := make([]Event, 0, len(j.pendingEmits))
	for _, queued := range j.pendingEmits {
		events = append(events, queued.event)
	}
	j.pendingEmits = nil
	return events
}

func (j *job) finish(answer string, turns int, total usage.Usage, terminalErr error) {
	j.finalOnce.Do(func() {
		j.eventMu.Lock()
		j.finished = true
		parent := j.deliveryCtx
		if parent == nil {
			parent = context.WithoutCancel(j.ctx)
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		var event Event
		if terminalErr != nil {
			if isCancellation(terminalErr) {
				event = Event{Type: "canceled", Message: "已停止本轮"}
			} else {
				var exceeded *maxIterationsError
				retryable := !errors.As(terminalErr, &exceeded)
				event = errorEvent(terminalErr, retryable)
				if exceeded != nil {
					event.MaxIterations = true
				}
			}
		} else {
			event = doneEvent(answer, turns, total.PromptTokens, total.CompletionTokens)
		}
		sendErr := j.stream.Send(ctx, event)
		if sendErr == nil && parent.Err() == nil {
			_ = CloseStreamGracefully(j.stream)
		} else {
			_ = j.stream.Close()
		}
		j.eventMu.Unlock()
		j.pendingMu.Lock()
		j.resumeIterator = nil
		j.pendingMu.Unlock()
	})
}

func (j *job) state() (bool, adk.AgentCancelFunc) {
	j.pendingMu.Lock()
	defer j.pendingMu.Unlock()
	return j.running, j.cancelFn
}
