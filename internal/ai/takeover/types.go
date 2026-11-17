package takeover

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/adk"
)

var (
	ErrNotFound        = errors.New("接管任务不存在或已结束")
	ErrStaleOwnership  = errors.New("接管令牌已过期")
	ErrOwnershipActive = errors.New("当前接管已有活动任务")
	ErrWriteDisabled   = errors.New("本次接管禁止终端写入")
	ErrResumeMismatch  = errors.New("接管恢复范围与终端当前资产不一致")
	ErrPauseInProgress = errors.New("接管仍在暂停中，AI 完成当前步骤后才能继续")
	errRunPaused       = errors.New("接管等待用户确认")
	errRunUserPaused   = errors.New("接管已被用户暂停")
	errPauseEscalated  = errors.New("接管暂停超时升级")
)

// Takeover phases reported to the frontend by pause and status.
const (
	PhaseIdle     = "idle"
	PhaseRunning  = "running"
	PhasePausing  = "pausing"
	PhasePaused   = "paused"
	PhaseConfirm  = "confirm"
	PhaseInactive = "inactive"
)

// Done statuses tell the frontend how a takeover ended without parsing the
// human-readable reason.
const (
	EndCompleted  = "completed"
	EndFailed     = "failed"
	EndIncomplete = "incomplete"
	EndStopped    = "stopped"
)

type Dependencies struct {
	Model       agent.ModelFactory
	Checkpoints adk.CheckPointStore
	Permission  func(context.Context) (guard.Config, error)
	// DeviceMode layers the guard mode bound to the real target asset (the
	// tab's resolved asset identity) over the global config; nil keeps the
	// plain global mode.
	DeviceMode             func(context.Context, guard.Config, string) (guard.Config, error)
	Grants                 *guard.Grants
	TabAsset               func(context.Context, string) (string, error)
	Snapshot               func(context.Context, string) (tools.Screen, error)
	WriteAI                func(context.Context, string, []byte) error
	TabSession             func(string) string
	Audit                  func(context.Context, tools.AuditEntry) error
	NewID                  func() string
	PollInterval           time.Duration
	PauseEscalationTimeout time.Duration
	Now                    func() time.Time
	// Shared marks a single-user desktop or explicitly shared deployment where
	// ownerless takeover is legitimate; server mode must leave it false so
	// requests without an account identity fail closed.
	Shared bool
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

// PauseResponse reports where a pause request left the takeover: pausing
// until the run reaches a safe point, already paused, waiting for a
// confirmation (nothing to pause), or idle (no job).
type PauseResponse struct {
	Phase string `json:"phase"`
}

// StatusResponse reports whether the ownership token still holds the tab and
// the phase of its job.
type StatusResponse struct {
	Active bool   `json:"active"`
	JobID  string `json:"jobId,omitempty"`
	Phase  string `json:"phase"`
}

type ownership struct {
	tabID   string
	token   string
	ownerID string
	jobID   string
	ctx     context.Context
	cancel  context.CancelFunc
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
	seq           uint64
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
	// changed is closed and cleared whenever running, userPaused or
	// pauseReady changes, so a resume can wait for a pause to settle.
	changed chan struct{}
	// resumeNote tells the next model call that the user paused and resumed.
	resumeNote bool
}

func (s *runState) changedLocked() chan struct{} {
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}

func (s *runState) notifyLocked() {
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
}

func (s *runState) phaseLocked() string {
	switch {
	case s.userPaused && s.pauseReady && !s.running:
		return PhasePaused
	case s.userPaused:
		return PhasePausing
	case s.running:
		return PhaseRunning
	default:
		return PhaseConfirm
	}
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
	// A sequence number is spent only on a delivered event: the frontend
	// buffers everything after a gap, so a send that failed (a canceled
	// iteration during pause) must not leave one.
	next := atomic.LoadUint64(&s.seq) + 1
	event.Seq = next
	if err := s.stream.Send(ctx, event); err != nil {
		return err
	}
	atomic.StoreUint64(&s.seq, next)
	return nil
}

func (s *runState) finish(event agent.Event) {
	s.finalOnce.Do(func() {
		s.eventMu.Lock()
		s.finished = true
		event.Seq = atomic.AddUint64(&s.seq, 1)
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
