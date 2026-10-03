package agent

import (
	"context"
	"errors"
	"sync"

	aicontext "github.com/ProbiusOfficial/NexTerm/internal/ai/context"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
)

var (
	ErrJobNotFound         = errors.New("AI 任务不存在或已结束")
	ErrConfirmationStale   = errors.New("确认已过期、重复或不属于当前工具调用")
	ErrInvalidConfirmation = errors.New("确认必须包含 callId、nonce 和有效 decision")
	errRunPaused           = errors.New("AI 任务等待用户交互")
)

type ModelFactory func(context.Context) (model.BaseChatModel, uint64, error)

type baseChatModelClient interface {
	BaseChatModel(context.Context) (model.BaseChatModel, uint64, error)
}

type toolCallingChatModelClient interface {
	ChatModel(context.Context) (model.ToolCallingChatModel, error)
}

func activeProfileModel(manager *profiles.Manager) ModelFactory {
	return func(ctx context.Context) (model.BaseChatModel, uint64, error) {
		profile, ok := manager.ActiveProfile()
		if !ok {
			return nil, 0, errors.New("未配置活动 AI 模型")
		}
		client, err := manager.ActiveClient()
		if err != nil {
			return nil, 0, err
		}
		if adapter, ok := any(client).(baseChatModelClient); ok {
			return adapter.BaseChatModel(ctx)
		}
		if adapter, ok := any(client).(toolCallingChatModelClient); ok {
			chatModel, err := adapter.ChatModel(ctx)
			return chatModel, profile.ContextWindow, err
		}
		return nil, 0, errors.New("当前 provider 未提供 Eino ChatModel 适配")
	}
}

type ConversationStore interface {
	ConvCreate(context.Context, string, any) (store.ConversationRow, error)
	ConvGet(context.Context, string) (store.ConversationRow, error)
	ConvList(context.Context) ([]store.ConversationRow, error)
	ConvRename(context.Context, string, string) error
	ConvTouch(context.Context, string) error
	ConvDelete(context.Context, string) error
	MsgInsert(context.Context, string, string, any, *int64, *int64) error
	MsgList(context.Context, string) ([]store.MessageRow, error)
}

type Config struct {
	Model           ModelFactory
	Profiles        *profiles.Manager
	Permissions     *guard.Manager
	Tools           *tools.Registry
	Context         *aicontext.Builder
	Store           ConversationStore
	Permission      func(context.Context) (guard.Config, error)
	FallbackCancel  func(string) error
	FallbackConfirm func(Confirmation) error
	Checkpoints     adk.CheckPointStore
	NewID           func() string
	MaxTurns        int
	MaxImages       int
	MaxImageBytes   int
}

type ChatArgs struct {
	ConversationID string      `json:"conversationId,omitempty"`
	ChannelID      string      `json:"-"`
	Scope          tools.Scope `json:"scope"`
	Message        string      `json:"message"`
	Selection      string      `json:"selection,omitempty"`
	Images         []string    `json:"images,omitempty"`
	PlanMode       bool        `json:"planMode,omitempty"`
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

type pendingRequest struct {
	callID string
	nonce  string
	kind   string
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

	eventMu      sync.Mutex
	finished     bool
	finalOnce    sync.Once
	completeOnce sync.Once
	pendingMu    sync.Mutex
	pending      *pendingRequest
	running      bool
}

func (j *job) emit(ctx context.Context, event Event) error {
	j.eventMu.Lock()
	defer j.eventMu.Unlock()
	if j.finished {
		return errors.New("AI job already finished")
	}
	return j.stream.Send(ctx, event)
}

func (j *job) finish(answer string, turns int, total usage.Usage, terminalErr error) {
	j.finalOnce.Do(func() {
		j.eventMu.Lock()
		j.finished = true
		ctx := j.deliveryCtx
		if ctx == nil {
			ctx = context.WithoutCancel(j.ctx)
		}
		var event Event
		if terminalErr != nil {
			event = errorEvent(terminalErr, !errors.Is(terminalErr, context.Canceled))
		} else {
			event = doneEvent(answer, turns, total.PromptTokens, total.CompletionTokens)
		}
		sendErr := j.stream.Send(ctx, event)
		if sendErr == nil && ctx.Err() == nil {
			_ = CloseStreamGracefully(j.stream)
		} else {
			_ = j.stream.Close()
		}
		j.eventMu.Unlock()
		j.pendingMu.Lock()
		j.pending = nil
		j.pendingMu.Unlock()
	})
}

func (j *job) setPending(pending *pendingRequest) {
	j.pendingMu.Lock()
	j.pending = pending
	j.pendingMu.Unlock()
}

func (j *job) setRunning(value bool) {
	j.pendingMu.Lock()
	j.running = value
	j.pendingMu.Unlock()
}

func (j *job) state() (*pendingRequest, bool, adk.AgentCancelFunc) {
	j.pendingMu.Lock()
	defer j.pendingMu.Unlock()
	return j.pending, j.running, j.cancelFn
}
