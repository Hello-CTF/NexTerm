package takeover

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/cloudwego/eino/adk"
)

type Manager struct {
	deps        Dependencies
	checkpoints adk.CheckPointStore
	mu          sync.Mutex
	owners      map[string]*ownership
	jobs        map[string]*runState
	locks       map[string]*sync.Mutex
	closed      bool
	wg          sync.WaitGroup
}

func NewManager(deps Dependencies) *Manager {
	if deps.NewID == nil {
		deps.NewID = ids.New
	}
	if deps.Permission == nil {
		deps.Permission = func(context.Context) (guard.Config, error) {
			return guard.Config{Mode: guard.ReadWrite}, nil
		}
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.PollInterval <= 0 {
		deps.PollInterval = 100 * time.Millisecond
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = agent.NewMemoryCheckpoints()
	}
	return &Manager{deps: deps, checkpoints: deps.Checkpoints, owners: make(map[string]*ownership), jobs: make(map[string]*runState), locks: make(map[string]*sync.Mutex)}
}

func (m *Manager) tabLock(tabID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock := m.locks[tabID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[tabID] = lock
	}
	return lock
}

func (m *Manager) Enter(ctx context.Context, tabID string) (string, error) {
	if tabID == "" || m.deps.Snapshot == nil {
		return "", errors.New("tabId 或终端服务无效")
	}
	if _, err := m.deps.Snapshot(ctx, tabID); err != nil {
		return "", err
	}
	owner, err := m.enterOwned(tabID)
	if err != nil {
		return "", err
	}
	return owner.token, nil
}

func (m *Manager) enterOwned(tabID string) (*ownership, error) {
	lock := m.tabLock(tabID)
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("接管服务已关闭")
	}
	previous := m.owners[tabID]
	ownerContext, cancel := context.WithCancel(context.Background())
	owner := &ownership{tabID: tabID, token: m.deps.NewID(), ctx: ownerContext, cancel: cancel}
	m.owners[tabID] = owner
	m.mu.Unlock()
	if previous != nil {
		previous.cancel()
	}
	if m.deps.Inject != nil {
		if err := m.deps.Inject(context.Background(), tabID, []byte(EnterBanner)); err != nil {
			m.mu.Lock()
			if m.owners[tabID] == owner {
				delete(m.owners, tabID)
			}
			m.mu.Unlock()
			cancel()
			return nil, err
		}
	}
	return owner, nil
}

func (m *Manager) Run(ctx context.Context, args RunArgs, factory agent.StreamFactory) (RunResponse, error) {
	if factory == nil || m.deps.Model == nil || m.deps.Snapshot == nil || m.deps.WriteAI == nil {
		return RunResponse{}, errors.New("接管 Eino ChatModel、终端或事件流未配置")
	}
	if args.TabID == "" || strings.TrimSpace(args.Instruction) == "" {
		return RunResponse{}, errors.New("tabId 和 instruction 不能为空")
	}
	if args.MaxSteps == 0 {
		args.MaxSteps = 30
	}
	if args.MaxSteps < 1 || args.MaxSteps > 100 {
		return RunResponse{}, errors.New("maxSteps 必须在 1-100 之间")
	}
	var owner *ownership
	var err error
	if args.Token == "" {
		owner, err = m.enterOwned(args.TabID)
	} else {
		m.mu.Lock()
		owner = m.owners[args.TabID]
		m.mu.Unlock()
		if owner == nil || owner.token != args.Token || owner.ctx.Err() != nil {
			return RunResponse{}, ErrStaleOwnership
		}
	}
	if err != nil {
		return RunResponse{}, err
	}
	jobID := m.deps.NewID()
	if strings.TrimSpace(jobID) == "" {
		return RunResponse{}, errors.New("接管 job ID 为空")
	}
	jobContext, cancel := context.WithCancel(owner.ctx)
	stream, err := factory(ctx, args.ChannelID, jobID)
	if err != nil {
		cancel()
		return RunResponse{}, err
	}
	if stream == nil {
		cancel()
		return RunResponse{}, errors.New("接管事件流为空")
	}
	stream = agent.WithEventSequence(stream)
	state := &runState{id: jobID, args: args, ctx: jobContext, cancel: cancel, owner: owner, stream: stream, memory: guard.NewMemory(), running: true}
	m.mu.Lock()
	if m.closed || m.owners[args.TabID] != owner || m.jobs[jobID] != nil {
		m.mu.Unlock()
		cancel()
		_ = stream.Close()
		return RunResponse{}, ErrStaleOwnership
	}
	m.jobs[jobID] = state
	m.wg.Add(1)
	m.mu.Unlock()
	go m.runJob(state)
	return RunResponse{JobID: jobID, Token: owner.token}, nil
}

func (m *Manager) Cancel(jobID string) error {
	m.mu.Lock()
	state := m.jobs[jobID]
	m.mu.Unlock()
	if state == nil {
		return ErrNotFound
	}
	m.cancelState(state, "用户取消")
	return nil
}

func (m *Manager) cancelState(state *runState, reason string) {
	state.setReason(reason)
	state.cancel()
	_, running, cancelFn := state.state()
	if cancelFn != nil && running {
		_, _ = cancelFn(adk.WithAgentCancelMode(adk.CancelImmediate))
	}
	if !running {
		m.complete(state, runResult{answer: reason, reason: reason})
	}
}

func (m *Manager) Confirm(confirmation agent.Confirmation) error {
	if confirmation.JobID == "" || confirmation.CallID == "" || confirmation.Nonce == "" {
		return agent.ErrInvalidConfirmation
	}
	switch confirmation.Decision {
	case "allow", "allow_session", "deny":
	default:
		return agent.ErrInvalidConfirmation
	}
	m.mu.Lock()
	state := m.jobs[confirmation.JobID]
	m.mu.Unlock()
	if state == nil || state.ctx.Err() != nil {
		return ErrNotFound
	}
	state.pendingMu.Lock()
	pending := state.pending
	if pending == nil || pending.callID != confirmation.CallID || pending.nonce != confirmation.Nonce || state.running || state.eino == nil {
		state.pendingMu.Unlock()
		return agent.ErrConfirmationStale
	}
	state.pending = nil
	state.eino.resume = &adk.ResumeParams{Targets: map[string]any{confirmation.Nonce: confirmation.Decision}}
	state.running = true
	state.pendingMu.Unlock()
	go m.runJob(state)
	return nil
}

func (m *Manager) Exit(ctx context.Context, tabID, token, reason string) error {
	if tabID == "" || token == "" {
		return ErrStaleOwnership
	}
	lock := m.tabLock(tabID)
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	owner := m.owners[tabID]
	if owner == nil || owner.token != token {
		m.mu.Unlock()
		return ErrStaleOwnership
	}
	delete(m.owners, tabID)
	m.mu.Unlock()
	if reason == "" {
		reason = "用户退出"
	}
	for _, state := range m.statesForOwner(owner) {
		m.cancelState(state, reason)
	}
	owner.cancel()
	return m.injectExit(ctx, tabID, reason)
}

func (m *Manager) statesForOwner(owner *ownership) []*runState {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*runState
	for _, state := range m.jobs {
		if state.owner == owner {
			result = append(result, state)
		}
	}
	return result
}

func (m *Manager) UserWrite(ctx context.Context, tabID string, data []byte) error {
	if m.deps.WriteUser == nil {
		return errors.New("用户终端写入未配置")
	}
	m.preemptOwnership(tabID, "用户夺回")
	lock := m.tabLock(tabID)
	lock.Lock()
	defer lock.Unlock()
	return m.deps.WriteUser(ctx, tabID, data)
}

func (m *Manager) Preempt(tabID string) {
	m.preemptOwnership(tabID, "用户夺回")
}

func (m *Manager) preemptOwnership(tabID, reason string) {
	m.mu.Lock()
	owner := m.owners[tabID]
	if owner != nil {
		delete(m.owners, tabID)
	}
	m.mu.Unlock()
	if owner == nil {
		return
	}
	for _, state := range m.statesForOwner(owner) {
		m.cancelState(state, reason)
	}
	owner.cancel()
	lock := m.tabLock(tabID)
	lock.Lock()
	defer lock.Unlock()
	_ = m.injectExit(context.Background(), tabID, reason)
}

func (m *Manager) injectExit(ctx context.Context, tabID, reason string) error {
	if m.deps.Inject == nil {
		return nil
	}
	return m.deps.Inject(ctx, tabID, []byte(fmt.Sprintf("\r\n\x1b[43;30m[接管结束: %s]\x1b[0m\r\n", reason)))
}

func (m *Manager) complete(state *runState, result runResult) {
	state.completeOnce.Do(func() {
		if result.err != nil {
			state.finish(agent.Event{Type: "error", Message: result.err.Error(), Retryable: !errors.Is(result.err, context.Canceled)})
		} else {
			state.finish(agent.Event{Type: "done", Answer: result.answer, Turns: result.steps})
		}
		m.cleanup(state, result.reason)
	})
}

func (m *Manager) cleanup(state *runState, reason string) {
	m.mu.Lock()
	if m.jobs[state.id] == state {
		delete(m.jobs, state.id)
	}
	current := m.owners[state.owner.tabID] == state.owner
	if current {
		delete(m.owners, state.owner.tabID)
	}
	m.mu.Unlock()
	if current {
		_ = m.injectExit(context.Background(), state.owner.tabID, reason)
		state.owner.cancel()
	}
	state.cancel()
	if deleter, ok := m.checkpoints.(adk.CheckPointDeleter); ok {
		_ = deleter.Delete(context.Background(), state.id)
	}
	m.wg.Done()
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	states := make([]*runState, 0, len(m.jobs))
	for _, state := range m.jobs {
		states = append(states, state)
	}
	m.mu.Unlock()
	for _, state := range states {
		m.cancelState(state, "服务关闭")
	}
	m.wg.Wait()
	return nil
}
