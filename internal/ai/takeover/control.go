package takeover

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func requestOwnerID(ctx context.Context) string {
	return ipc.OwnerID(ctx)
}

func linkedRequestContext(requestCtx, ownerCtx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(requestCtx))
	stop := context.AfterFunc(ownerCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (m *Manager) authorizeTab(ctx context.Context, tabID string) error {
	if tabID == "" || m.deps.Snapshot == nil {
		return errors.New("tabId 或终端服务无效")
	}
	_, err := m.deps.Snapshot(ctx, tabID)
	return err
}

func (m *Manager) authorizeOwnership(ctx context.Context, tabID, token string) (*ownership, error) {
	if token == "" {
		return nil, ErrStaleOwnership
	}
	if err := m.authorizeTab(ctx, tabID); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("接管服务已关闭")
	}
	owner := m.owners[tabID]
	if owner == nil || owner.token != token || owner.ownerID != requestOwnerID(ctx) || owner.ctx.Err() != nil {
		return nil, ErrStaleOwnership
	}
	return owner, nil
}

func (m *Manager) authorizeJob(ctx context.Context, jobID string) (*runState, error) {
	m.mu.Lock()
	state := m.jobs[jobID]
	m.mu.Unlock()
	if state == nil {
		return nil, ErrNotFound
	}
	if state.owner.ownerID != requestOwnerID(ctx) {
		return nil, ErrStaleOwnership
	}
	if err := m.authorizeTab(ctx, state.args.TabID); err != nil {
		return nil, err
	}
	m.mu.Lock()
	current := !m.closed && m.owners[state.args.TabID] == state.owner
	m.mu.Unlock()
	if !current || state.ctx.Err() != nil {
		return nil, ErrNotFound
	}
	return state, nil
}

func (m *Manager) ConfirmContext(ctx context.Context, confirmation agent.Confirmation) error {
	if confirmation.JobID == "" {
		return agent.ErrInvalidConfirmation
	}
	if _, err := m.authorizeJob(ctx, confirmation.JobID); err != nil {
		return err
	}
	return m.Confirm(confirmation)
}

func (m *Manager) CancelContext(ctx context.Context, jobID string) error {
	state, err := m.authorizeJob(ctx, jobID)
	if err != nil {
		return err
	}
	m.cancelState(state, "用户取消")
	return nil
}

func (m *Manager) PauseFor(ctx context.Context, tabID, token string) error {
	owner, err := m.authorizeOwnership(ctx, tabID, token)
	if err != nil {
		return err
	}
	for _, state := range m.statesForOwner(owner) {
		m.pauseState(state)
	}
	return nil
}
