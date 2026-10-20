package takeover

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func requestOwnerID(ctx context.Context) string {
	return ipc.OwnerID(ctx)
}

// resolveOwner fails closed when the deployment requires an accountable
// identity: server mode rejects requests without an account instead of
// letting them act as an empty owner; shared deployments stay compatible.
func (m *Manager) resolveOwner(ctx context.Context) (string, error) {
	return owner.ID(ctx, m.deps.Shared)
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
	if _, err := m.resolveOwner(ctx); err != nil {
		return err
	}
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
	if _, err := m.resolveOwner(ctx); err != nil {
		return nil, err
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
	if _, err := m.resolveOwner(ctx); err != nil {
		return nil, err
	}
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

// verifyJobAsset re-resolves the tab's current asset and requires it to match
// the identity the job was bound to; an unresolvable or changed asset fails
// closed. Jobs without asset evidence (or without a resolver) stay unknown and
// keep their previous behavior.
func (m *Manager) verifyJobAsset(ctx context.Context, state *runState) error {
	if state.assetID == "" || m.deps.TabAsset == nil {
		return nil
	}
	current, err := m.deps.TabAsset(ctx, state.args.TabID)
	if err != nil {
		return err
	}
	if current == "" || current != state.assetID {
		return ErrResumeMismatch
	}
	return nil
}

func (m *Manager) ConfirmContext(ctx context.Context, confirmation agent.Confirmation) error {
	if confirmation.JobID == "" {
		return agent.ErrInvalidConfirmation
	}
	if _, err := m.authorizeJob(ctx, confirmation.JobID); err != nil {
		return err
	}
	return m.confirm(ctx, confirmation)
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
