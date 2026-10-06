package takeover

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/adk"
)

var (
	ErrNotFound        = errors.New("接管任务不存在或已结束")
	ErrStaleOwnership  = errors.New("接管令牌已过期")
	ErrOwnershipActive = errors.New("当前接管已有活动任务")
	ErrWriteDisabled   = errors.New("本次接管禁止终端写入")
	ErrResumeMismatch  = errors.New("接管恢复范围与终端当前资产不一致")
	errRunPaused       = errors.New("接管等待用户确认")
	errRunUserPaused   = errors.New("接管已被用户暂停")
	errPauseEscalated  = errors.New("接管暂停超时升级")
)

const EnterBanner = "\r\n\x1b[41;37m[AI 正在操作此终端 — 按 Esc 或任意键暂停]\x1b[0m\r\n"

type Dependencies struct {
	Model                  agent.ModelFactory
	Checkpoints            adk.CheckPointStore
	Permission             func(context.Context) (guard.Config, error)
	Grants                 *guard.Grants
	TabAsset               func(context.Context, string) (string, error)
	Snapshot               func(context.Context, string) (tools.Screen, error)
	WriteAI                func(context.Context, string, []byte) error
	Inject                 func(context.Context, string, []byte) error
	TabSession             func(string) string
	Audit                  func(context.Context, tools.AuditEntry) error
	NewID                  func() string
	PollInterval           time.Duration
	PauseEscalationTimeout time.Duration
	Now                    func() time.Time
}

type RunArgs struct {
	TabID       string `json:"tabId"`
	Token       string `json:"token,omitempty"`
	ChannelID   string `json:"-"`
	Instruction string `json:"instruction"`
	AllowWrite  *bool  `json:"allowWrite,omitempty"`
	MaxSteps    int    `json:"maxSteps,omitempty"`
}

type RunResponse struct {
	JobID string `json:"jobId"`
	Token string `json:"token"`
}

type ownership struct {
	tabID  string
	token  string
	jobID  string
	ctx    context.Context
	cancel context.CancelFunc
}

type pending struct {
	callID string
	nonce  string
}

type runState struct {
	id            string
	args          RunArgs
	ctx           context.Context
	cancel        context.CancelFunc
	deliveryCtx   context.Context
	forceCancel   context.CancelFunc
	owner         *ownership
	stream        agent.Stream
	memory        *guard.Memory
	eino          *takeoverRuntime
	cancelFn      adk.AgentCancelFunc
	eventMu       sync.Mutex
	finished      bool
	finalOnce     sync.Once
	finalizeMu    sync.Mutex
	completed     bool
	pendingMu     sync.Mutex
	pending       *pending
	running       bool
	userPaused    bool
	pauseReady    bool
	started       bool
	assetID       string
	iterCtx       context.Context
	iterCancel    context.CancelFunc
	cancelOutcome chan error
	reasonMu      sync.Mutex
	reason        string
}

func (s *runState) setReason(reason string) {
	s.reasonMu.Lock()
	if s.reason == "" {
		s.reason = reason
	}
	s.reasonMu.Unlock()
}

func (s *runState) stopReason() string {
	s.reasonMu.Lock()
	defer s.reasonMu.Unlock()
	return s.reason
}

func (s *runState) installIterationLocked() {
	s.iterCtx, s.iterCancel = context.WithCancel(s.ctx)
}

func (s *runState) emit(ctx context.Context, event agent.Event) error {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.finished {
		return errors.New("takeover already finished")
	}
	return s.stream.Send(ctx, event)
}

func (s *runState) finish(event agent.Event) {
	s.finalOnce.Do(func() {
		s.eventMu.Lock()
		s.finished = true
		parent := s.deliveryCtx
		if parent == nil {
			parent = context.WithoutCancel(s.ctx)
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		sendErr := s.stream.Send(ctx, event)
		if sendErr == nil && parent.Err() == nil {
			_ = agent.CloseStreamGracefully(s.stream)
		} else {
			_ = s.stream.Close()
		}
		s.eventMu.Unlock()
		s.pendingMu.Lock()
		s.pending = nil
		s.pendingMu.Unlock()
	})
}
