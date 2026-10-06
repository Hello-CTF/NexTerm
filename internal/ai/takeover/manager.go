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
	deps           Dependencies
	checkpoints    adk.CheckPointStore
	mu             sync.Mutex
	owners         map[string]*ownership
	jobs           map[string]*runState
	reservedJobs   map[string]struct{}
	locks          map[string]*sync.Mutex
	operationLocks map[string]*sync.Mutex
	closed         bool
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
	if deps.PauseEscalationTimeout <= 0 {
		deps.PauseEscalationTimeout = 30 * time.Second
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = agent.NewMemoryCheckpoints()
	}
	return &Manager{deps: deps, checkpoints: &detachedCheckpoints{CheckPointStore: deps.Checkpoints}, owners: make(map[string]*ownership), jobs: make(map[string]*runState), reservedJobs: make(map[string]struct{}), locks: make(map[string]*sync.Mutex), operationLocks: make(map[string]*sync.Mutex)}
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

func (m *Manager) operationLock(tabID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock := m.operationLocks[tabID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.operationLocks[tabID] = lock
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
	operation := m.operationLock(tabID)
	operation.Lock()
	defer operation.Unlock()
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
		for _, state := range m.statesForOwner(previous) {
			m.cancelState(state, "新的接管")
		}
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

func (m *Manager) reserveJob(owner *ownership, jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.owners[owner.tabID] != owner {
		return ErrStaleOwnership
	}
	if _, exists := m.reservedJobs[jobID]; m.jobs[jobID] != nil || exists {
		return errors.New("接管 job ID 重复")
	}
	if owner.jobID != "" {
		return ErrOwnershipActive
	}
	for _, state := range m.jobs {
		if state.owner == owner {
			return ErrOwnershipActive
		}
	}
	m.reservedJobs[jobID] = struct{}{}
	owner.jobID = jobID
	return nil
}

func (m *Manager) releaseJob(owner *ownership, jobID string) {
	m.mu.Lock()
	delete(m.reservedJobs, jobID)
	if owner.jobID == jobID {
		owner.jobID = ""
	}
	m.mu.Unlock()
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
	if err := m.reserveJob(owner, jobID); err != nil {
		return RunResponse{}, err
	}
	jobContext, cancel := context.WithCancel(owner.ctx)
	deliveryContext, forceCancel := context.WithCancel(context.WithoutCancel(jobContext))
	stream, err := factory(ctx, args.ChannelID, jobID)
	if err != nil {
		m.releaseJob(owner, jobID)
		cancel()
		forceCancel()
		return RunResponse{}, err
	}
	if stream == nil {
		m.releaseJob(owner, jobID)
		cancel()
		forceCancel()
		return RunResponse{}, errors.New("接管事件流为空")
	}
	stream = agent.WithEventSequence(stream)
	assetID := ""
	if m.deps.TabAsset != nil {
		id, err := m.deps.TabAsset(ctx, args.TabID)
		if err != nil {
			m.releaseJob(owner, jobID)
			cancel()
			forceCancel()
			_ = stream.Close()
			return RunResponse{}, err
		}
		assetID = id
	}
	jobIterCtx, jobIterCancel := context.WithCancel(jobContext)
	state := &runState{id: jobID, args: args, ctx: jobContext, cancel: cancel, deliveryCtx: deliveryContext, forceCancel: forceCancel, owner: owner, stream: stream, memory: guard.NewMemory(), running: true, assetID: assetID, iterCtx: jobIterCtx, iterCancel: jobIterCancel}
	m.mu.Lock()
	if m.closed || m.owners[args.TabID] != owner || m.jobs[jobID] != nil {
		m.mu.Unlock()
		m.releaseJob(owner, jobID)
		cancel()
		forceCancel()
		_ = stream.Close()
		return RunResponse{}, ErrStaleOwnership
	}
	for _, existing := range m.jobs {
		if existing.owner == owner {
			m.mu.Unlock()
			m.releaseJob(owner, jobID)
			cancel()
			forceCancel()
			_ = stream.Close()
			return RunResponse{}, ErrOwnershipActive
		}
	}
	m.jobs[jobID] = state
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

func (m *Manager) Pause(tabID string) {
	m.mu.Lock()
	owner := m.owners[tabID]
	m.mu.Unlock()
	if owner == nil {
		return
	}
	for _, state := range m.statesForOwner(owner) {
		m.pauseState(state)
	}
}

func (m *Manager) pauseState(state *runState) {
	state.pendingMu.Lock()
	if !state.running {
		state.pendingMu.Unlock()
		return
	}
	state.userPaused = true
	state.pauseReady = false
	cancelFn := state.cancelFn
	state.pendingMu.Unlock()
	go m.pauseWatchdog(state)
	if cancelFn != nil {
		m.cancelIteration(cancelFn, state, false)
	}
}

func (m *Manager) pauseWatchdog(state *runState) {
	timer := time.NewTimer(m.deps.PauseEscalationTimeout + 3*time.Second)
	defer timer.Stop()
	<-timer.C
	state.finalizeMu.Lock()
	defer state.finalizeMu.Unlock()
	if state.completed {
		return
	}
	state.pendingMu.Lock()
	stuck := state.userPaused && !state.pauseReady
	state.pendingMu.Unlock()
	if stuck {
		m.completeLocked(state, runResult{err: errors.New("暂停超时：模型未在安全点内响应，任务已停止")})
	}
}

func (m *Manager) cancelIteration(cancelFn adk.AgentCancelFunc, state *runState, immediate bool) {
	options := []adk.AgentCancelOption{
		adk.WithAgentCancelMode(adk.CancelAfterChatModel | adk.CancelAfterToolCalls),
		adk.WithAgentCancelTimeout(m.deps.PauseEscalationTimeout),
	}
	if immediate {
		options = []adk.AgentCancelOption{adk.WithAgentCancelMode(adk.CancelImmediate)}
	}
	handle, _ := cancelFn(options...)
	go func() {
		var outcome error
		if handle != nil {
			outcome = handle.Wait()
		}
		state.pendingMu.Lock()
		outcomeCh := state.cancelOutcome
		iterCancel := state.iterCancel
		state.pendingMu.Unlock()
		if outcomeCh != nil {
			outcomeCh <- outcome
		}
		if iterCancel != nil {
			iterCancel()
		}
	}()
}

type ResumeArgs struct {
	TabID     string `json:"tabId"`
	Token     string `json:"token"`
	JobID     string `json:"jobId"`
	ChannelID string `json:"-"`
}

func (m *Manager) Resume(ctx context.Context, args ResumeArgs, factory agent.StreamFactory) (RunResponse, error) {
	if factory == nil || m.deps.Model == nil || m.deps.Snapshot == nil || m.deps.WriteAI == nil {
		return RunResponse{}, errors.New("接管 Eino ChatModel、终端或事件流未配置")
	}
	if args.TabID == "" || args.Token == "" || args.JobID == "" {
		return RunResponse{}, errors.New("tabId、token 和 jobId 不能为空")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return RunResponse{}, errors.New("接管服务已关闭")
	}
	owner := m.owners[args.TabID]
	existing := m.jobs[args.JobID]
	m.mu.Unlock()
	if owner == nil || owner.token != args.Token || owner.ctx.Err() != nil {
		return RunResponse{}, ErrStaleOwnership
	}
	if existing != nil {
		return m.resumePaused(ctx, owner, existing, args, factory)
	}
	return m.recoverJob(ctx, owner, args, factory)
}

func (m *Manager) resumePaused(ctx context.Context, owner *ownership, state *runState, args ResumeArgs, factory agent.StreamFactory) (RunResponse, error) {
	if state.owner != owner {
		return RunResponse{}, ErrStaleOwnership
	}
	state.pendingMu.Lock()
	if !(state.userPaused && !state.running && state.pauseReady) {
		state.pendingMu.Unlock()
		return RunResponse{}, ErrOwnershipActive
	}
	state.userPaused = false
	state.pauseReady = false
	state.running = true
	state.installIterationLocked()
	state.pendingMu.Unlock()
	stream, err := factory(ctx, args.ChannelID, state.id)
	if err != nil {
		state.pendingMu.Lock()
		state.running = false
		state.userPaused = true
		state.pauseReady = true
		state.pendingMu.Unlock()
		return RunResponse{}, err
	}
	if stream == nil {
		state.pendingMu.Lock()
		state.running = false
		state.userPaused = true
		state.pauseReady = true
		state.pendingMu.Unlock()
		return RunResponse{}, errors.New("接管事件流为空")
	}
	stream = agent.WithEventSequence(stream)
	state.eventMu.Lock()
	previous := state.stream
	state.stream = stream
	state.eventMu.Unlock()
	if previous != stream {
		_ = previous.Close()
	}
	go m.runJob(state)
	return RunResponse{JobID: state.id, Token: owner.token}, nil
}

func (m *Manager) recoverJob(ctx context.Context, owner *ownership, args ResumeArgs, factory agent.StreamFactory) (RunResponse, error) {
	record, err := m.loadRecovery(ctx, args.JobID)
	if err != nil {
		return RunResponse{}, err
	}
	if record.TabID != args.TabID {
		return RunResponse{}, ErrResumeMismatch
	}
	if _, found, err := m.checkpoints.Get(ctx, args.JobID); err != nil {
		return RunResponse{}, err
	} else if !found {
		m.deleteRecovery(context.WithoutCancel(ctx), args.JobID)
		return RunResponse{}, ErrNotFound
	}
	assetID := ""
	if m.deps.TabAsset != nil {
		if assetID, err = m.deps.TabAsset(ctx, args.TabID); err != nil {
			return RunResponse{}, err
		}
	}
	if record.AssetID == "" || assetID == "" || record.AssetID != assetID {
		return RunResponse{}, ErrResumeMismatch
	}
	if err := m.reserveJob(owner, args.JobID); err != nil {
		return RunResponse{}, err
	}
	stream, err := factory(ctx, args.ChannelID, args.JobID)
	if err != nil {
		m.releaseJob(owner, args.JobID)
		return RunResponse{}, err
	}
	if stream == nil {
		m.releaseJob(owner, args.JobID)
		return RunResponse{}, errors.New("接管事件流为空")
	}
	stream = agent.WithEventSequence(stream)
	jobContext, cancel := context.WithCancel(owner.ctx)
	deliveryContext, forceCancel := context.WithCancel(context.WithoutCancel(jobContext))
	jobIterCtx, jobIterCancel := context.WithCancel(jobContext)
	state := &runState{
		id: args.JobID, ctx: jobContext, cancel: cancel, deliveryCtx: deliveryContext, forceCancel: forceCancel,
		owner: owner, stream: stream, memory: guard.NewMemory(), running: true, started: true, assetID: assetID,
		iterCtx: jobIterCtx, iterCancel: jobIterCancel,
		args: RunArgs{TabID: args.TabID, ChannelID: args.ChannelID, Instruction: record.Instruction, AllowWrite: record.AllowWrite, MaxSteps: record.MaxSteps},
	}
	m.mu.Lock()
	if m.closed || m.owners[args.TabID] != owner || m.jobs[args.JobID] != nil {
		m.mu.Unlock()
		m.releaseJob(owner, args.JobID)
		cancel()
		forceCancel()
		_ = stream.Close()
		return RunResponse{}, ErrStaleOwnership
	}
	m.jobs[args.JobID] = state
	m.mu.Unlock()
	go m.runJob(state)
	return RunResponse{JobID: args.JobID, Token: owner.token}, nil
}

func (m *Manager) cancelState(state *runState, reason string) {
	state.setReason(reason)
	state.cancel()
	state.pendingMu.Lock()
	running := state.running
	cancelFn := state.cancelFn
	if !running {
		state.pending = nil
	}
	state.pendingMu.Unlock()
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
	if state.ctx.Err() != nil {
		state.pendingMu.Unlock()
		return ErrNotFound
	}
	pending := state.pending
	if pending == nil || pending.callID != confirmation.CallID || pending.nonce != confirmation.Nonce || state.eino == nil {
		state.pendingMu.Unlock()
		return agent.ErrConfirmationStale
	}
	start := !state.running
	state.pending = nil
	state.eino.resume = &adk.ResumeParams{Targets: map[string]any{confirmation.Nonce: confirmation.Decision}}
	if start {
		state.running = true
		state.installIterationLocked()
	}
	state.pendingMu.Unlock()
	if start {
		go m.runJob(state)
	}
	return nil
}

func (m *Manager) Exit(ctx context.Context, tabID, token, reason string) error {
	if tabID == "" || token == "" {
		return ErrStaleOwnership
	}
	operation := m.operationLock(tabID)
	operation.Lock()
	defer operation.Unlock()
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

func (m *Manager) Preempt(tabID string) {
	m.preemptOwnership(tabID, "用户夺回")
}

func (m *Manager) preemptOwnership(tabID, reason string) {
	operation := m.operationLock(tabID)
	operation.Lock()
	defer operation.Unlock()
	m.preemptOwnershipLocked(tabID, reason)
}

func (m *Manager) preemptOwnershipLocked(tabID, reason string) {
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
	_ = m.injectExit(context.Background(), tabID, reason)
}

func (m *Manager) injectExit(ctx context.Context, tabID, reason string) error {
	if m.deps.Inject == nil {
		return nil
	}
	return m.deps.Inject(ctx, tabID, []byte(fmt.Sprintf("\r\n\x1b[43;30m[接管结束: %s]\x1b[0m\r\n", reason)))
}

func (m *Manager) complete(state *runState, result runResult) {
	state.finalizeMu.Lock()
	defer state.finalizeMu.Unlock()
	m.completeLocked(state, result)
}

func (m *Manager) completeLocked(state *runState, result runResult) {
	if state.completed {
		return
	}
	state.completed = true
	m.cleanupRecords(state, result.reason)
	if result.err != nil {
		state.finish(agent.Event{Type: "error", Message: result.err.Error(), Retryable: !errors.Is(result.err, context.Canceled)})
	} else {
		state.finish(agent.Event{Type: "done", Answer: result.answer, Turns: result.steps})
	}
	m.removeJob(state)
	state.cancel()
	if state.forceCancel != nil {
		state.forceCancel()
	}
}

func (m *Manager) cleanupRecords(state *runState, reason string) {
	m.mu.Lock()
	current := m.owners[state.owner.tabID] == state.owner
	m.mu.Unlock()
	if current {
		_ = m.injectExit(context.Background(), state.owner.tabID, reason)
		state.owner.cancel()
	}
	if deleter, ok := m.checkpoints.(adk.CheckPointDeleter); ok {
		_ = deleter.Delete(context.Background(), state.id)
		_ = deleter.Delete(context.Background(), recoveryKey(state.id))
		_ = deleter.Delete(context.Background(), execKey(state.id))
	}
}

func (m *Manager) removeJob(state *runState) {
	m.mu.Lock()
	if m.jobs[state.id] == state {
		delete(m.jobs, state.id)
	}
	delete(m.reservedJobs, state.id)
	if state.owner.jobID == state.id {
		state.owner.jobID = ""
	}
	if m.owners[state.owner.tabID] == state.owner {
		delete(m.owners, state.owner.tabID)
	}
	m.mu.Unlock()
}

func (m *Manager) Close() error {
	return m.CloseContext(context.Background())
}

func (m *Manager) CloseContext(ctx context.Context) error {
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
		m.pauseState(state)
		m.persistRecovery(state)
		if state.forceCancel != nil {
			state.forceCancel()
		}
	}
	for {
		running := false
		m.mu.Lock()
		for _, state := range m.jobs {
			state.pendingMu.Lock()
			running = running || state.running
			state.pendingMu.Unlock()
		}
		m.mu.Unlock()
		if !running {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	m.mu.Lock()
	for id, state := range m.jobs {
		delete(m.jobs, id)
		state.cancel()
	}
	m.mu.Unlock()
	for _, state := range states {
		_ = state.stream.Close()
	}
	return nil
}
