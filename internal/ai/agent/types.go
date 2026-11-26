package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	aicontext "github.com/Hello-CTF/NexTerm/internal/ai/context"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
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
	ErrJobForbidden        = errors.New("无权操作该 AI 任务")
	ErrConfirmationStale   = errors.New("确认已过期、重复或不属于当前工具调用")
	ErrInvalidConfirmation = errors.New("确认必须包含 callId、nonce 和有效 decision")
	ErrSteerQueueFull      = errors.New("AI 补充指令队列已满，请稍后再试")
	errRunPaused           = errors.New("AI 任务等待用户交互")
	// errRunShutdown records a run stopped because the app closed; it reuses
	// the restart wording and stays retryable.
	errRunShutdown = errors.New("AI 任务因应用重启而中断")
)

type openAccessContextKey struct{}

// WithOpenAccess marks an already-admitted open or trusted internal caller.
func WithOpenAccess(ctx context.Context) context.Context {
	return owner.WithOpenAccess(context.WithValue(ctx, openAccessContextKey{}, true))
}

func openAccessFromContext(ctx context.Context) bool {
	open, _ := ctx.Value(openAccessContextKey{}).(bool)
	return open
}

type ModelFactory func(context.Context) (model.BaseChatModel, uint64, error)

type ProfileModelFactory func(context.Context, string) (model.BaseChatModel, uint64, error)

type baseChatModelClient interface {
	BaseChatModel(context.Context) (model.BaseChatModel, uint64, error)
}

func activeProfileModel(manager *profiles.Manager) ModelFactory {
	return func(ctx context.Context) (model.BaseChatModel, uint64, error) {
		if _, ok, err := manager.ActiveProfile(ctx); err != nil {
			return nil, 0, err
		} else if !ok {
			return nil, 0, errors.New("未配置活动 AI 模型")
		}
		client, err := manager.ActiveClient(ctx)
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
	ScopeValidator        ScopeValidator
	ScopeResolver         ScopeResolver
	Store                 ConversationStore
	Runs                  RunStore
	TitleTimeout          time.Duration
	Permission            func(context.Context) (guard.Config, error)
	Checkpoints           adk.CheckPointStore
	HITL                  *hitl.Manager
	HITLTTL               time.Duration
	HITLTerminalRetention time.Duration
	// HITLAssetResolver resolves a run's real asset identity for the tool
	// awaiting confirmation from server-side state; nil disables asset
	// evidence binding.
	HITLAssetResolver func(context.Context, string, string) (string, error)
	// HITLRequestAssetResolver takes precedence and binds the actual target
	// selected by the persisted tool parameters.
	HITLRequestAssetResolver func(context.Context, string, string, json.RawMessage) (string, error)
	// HITLEnvironmentResolver resolves the production environment names a
	// confirmation must type back for the run's target asset, from the
	// request owner and the real asset through the configurable production
	// classification; nil issues no environment challenge.
	HITLEnvironmentResolver func(context.Context, string) ([]string, error)
	// Roster is the conversation's persistent agent roster; nil disables
	// roster context injection. When configured, every job start loads the
	// roster and a failed load fails the start closed instead of silently
	// running without roster context.
	Roster        *Roster
	Now           func() time.Time
	Subagents     *tools.SubagentConfig
	NewID         func() string
	MaxTurns      int
	MaxImages     int
	MaxImageBytes int

	MaxPendingSteers int

	// Settings holds the user's step limit, read when each run starts; MaxTurns
	// applies when it holds no valid value.
	Settings RunSettingsStore

	Memory         *memory.Store
	MemoryScope    memory.Scope
	MemoryScopeFor func(context.Context) (memory.Scope, error)

	// ServerMode marks nexterm-server assembly (false on desktop): grant state is
	// isolated per account and mutations require an account or explicit Open identity.
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
	// ModelProfileID picks the model profile of this run; empty uses the
	// default profile. The AI panel sends it so a model chosen there applies
	// to its conversation without changing the default.
	ModelProfileID string `json:"modelProfileId,omitempty"`
	// Privacy is the conversation's temporary redaction choice ("on", "off");
	// empty follows the model profile and the owner's switch.
	Privacy provider.PrivacyOverride `json:"privacy,omitempty"`
	// Locale is the sender's interface language; the reply follows the
	// user's own language and falls back to it when that is unclear.
	Locale string `json:"locale,omitempty"`
}

type RefArg struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Label     string `json:"label"`
	SessionID string `json:"sessionId,omitempty"`
	Path      string `json:"path,omitempty"`
	Text      string `json:"text,omitempty"`
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
	// EnvironmentNames are the actual environment names the user typed back
	// for a production-classified target; they are validated against the
	// challenge's required names at resume and are never auto-echoed.
	EnvironmentNames []string `json:"environmentNames,omitempty"`
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
	ownerID     string
	args        ChatArgs
	ctx         context.Context
	cancel      context.CancelFunc
	deliveryCtx context.Context
	forceCancel context.CancelFunc
	stream      Stream
	// downstream is the live delivery below the journal; a confirmation from
	// a reopened panel switches it to that panel's channel.
	downstream *switchableStream
	memory     *guard.Memory
	// shutdown marks a run stopped because the app is closing; it is recorded
	// as interrupted and retryable rather than canceled by the user.
	shutdown atomic.Bool
	// settled is set once the run's final status is recorded, right before
	// its terminal event is delivered: the run writes nothing more to the
	// conversation, so a client may start the next run as soon as it sees
	// the terminal event even though cleanup is still finishing.
	settled atomic.Bool
	// pendingCalls are the call IDs of interactions shown to the user and not
	// resolved yet, so an expiry can settle each card.
	pendingCallsMu sync.Mutex
	pendingCalls   []string
	resolved       []Event
	eino           *einoRuntime
	cancelFn       adk.AgentCancelFunc
	cancelSync     *cancelSync
	capture        *Capture

	steer *steer.Queue

	// streamedMu guards streamed: the assistant text of the current model
	// turn that reached the client but is not saved yet. A run that ends
	// without a final answer saves it as a partial answer.
	streamedMu sync.Mutex
	streamed   strings.Builder
	// streamedReasoning is the reasoning text of the same turn.
	streamedReasoning strings.Builder

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
	doneCh         chan struct{}
	doneOnce       sync.Once

	// callHandles remembers the roster handle resolved for each tool call of
	// this run, so the toolResult event carries the same reference the
	// toolCall event did.
	callHandlesMu sync.Mutex
	callHandles   map[string]string
}

func (j *job) rememberCallHandle(callID, handle string) {
	if callID == "" || handle == "" {
		return
	}
	j.callHandlesMu.Lock()
	defer j.callHandlesMu.Unlock()
	if j.callHandles == nil {
		j.callHandles = make(map[string]string)
	}
	j.callHandles[callID] = handle
}

func (j *job) callHandle(callID string) string {
	j.callHandlesMu.Lock()
	defer j.callHandlesMu.Unlock()
	return j.callHandles[callID]
}

func (j *job) emit(ctx context.Context, event Event) error {
	j.eventMu.Lock()
	defer j.eventMu.Unlock()
	if j.finished {
		return errors.New("AI job already finished")
	}
	return j.stream.Send(ctx, event)
}

func (j *job) recordStreamed(text string) {
	j.streamedMu.Lock()
	j.streamed.WriteString(text)
	j.streamedMu.Unlock()
}

func (j *job) recordStreamedReasoning(text string) {
	j.streamedMu.Lock()
	j.streamedReasoning.WriteString(text)
	j.streamedMu.Unlock()
}

func (j *job) resetStreamed() {
	j.streamedMu.Lock()
	j.streamed.Reset()
	j.streamedReasoning.Reset()
	j.streamedMu.Unlock()
}

// takeStreamed returns the unsaved streamed text and clears it, so it is
// saved at most once.
func (j *job) takeStreamed() string {
	text, _ := j.takeStreamedWithReasoning()
	return text
}

// takeStreamedWithReasoning is takeStreamed plus the turn's reasoning text.
func (j *job) takeStreamedWithReasoning() (string, string) {
	j.streamedMu.Lock()
	defer j.streamedMu.Unlock()
	text, reasoning := j.streamed.String(), j.streamedReasoning.String()
	j.streamed.Reset()
	j.streamedReasoning.Reset()
	return text, reasoning
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

func (j *job) finish(answer string, turns int, total usage.Usage, terminalErr error, beforeClose ...func()) (finishErr error) {
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
		statusRecorded := false
		beforeDeliver := func() {
			if statusRecorded {
				return
			}
			statusRecorded = true
			if len(beforeClose) > 0 && beforeClose[0] != nil {
				beforeClose[0]()
			}
		}
		var sendErr error
		if terminal, ok := j.stream.(interface {
			SendTerminal(context.Context, context.Context, Event, func()) error
		}); ok {
			sendErr = terminal.SendTerminal(ctx, parent, event, beforeDeliver)
		} else {
			beforeDeliver()
			liveCtx, liveCancel := context.WithTimeout(parent, 5*time.Second)
			sendErr = j.stream.Send(liveCtx, event)
			liveCancel()
		}
		beforeDeliver()
		var flushErr error
		if flusher, ok := j.stream.(interface{ FlushPending() error }); ok {
			flushErr = flusher.FlushPending()
		}
		var closeErr error
		var fallback *terminalFallbackError
		if (sendErr == nil && parent.Err() == nil) || errors.As(sendErr, &fallback) {
			closeErr = CloseStreamGracefully(j.stream)
		} else {
			closeErr = j.stream.Close()
		}
		finishErr = errors.Join(sendErr, flushErr, closeErr)
		j.eventMu.Unlock()
		j.pendingMu.Lock()
		j.resumeIterator = nil
		j.pendingMu.Unlock()
	})
	return finishErr
}

func (j *job) state() (bool, adk.AgentCancelFunc) {
	j.pendingMu.Lock()
	defer j.pendingMu.Unlock()
	return j.running, j.cancelFn
}

func (j *job) markDone() {
	j.doneOnce.Do(func() {
		if j.doneCh != nil {
			close(j.doneCh)
		}
	})
}
