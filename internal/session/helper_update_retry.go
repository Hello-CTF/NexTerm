package session

import (
	"context"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// helperUpdateRetryTimeout bounds the background helper update started when a session's last durable terminal ends.
const helperUpdateRetryTimeout = 10 * time.Minute

// updateWaitingProvider is a durable provider whose helper update waits only for the helper's processes to end.
type updateWaitingProvider interface {
	UpdateWaiting() bool
}

// retryWaitingHelperUpdate resolves the SSH helper again once the session runs no live durable terminal, when the
// helper it uses deferred its update only because terminals were running. The resolver checks again that the whole
// helper is idle before it switches builds, so terminals of other clients keep it deferred. The session keeps the
// current provider unless the new resolution succeeds.
func (m *Manager) retryWaitingHelperUpdate(session *Session) {
	if m.resolver == nil || session == nil {
		return
	}
	session.mu.Lock()
	transport, generation := session.transport, session.generation
	eligible := !session.closed && session.status == StatusConnected && session.asset.Kind == KindSSH &&
		transport != nil && !session.helperUpdateRetrying
	tabs := make([]*Tab, 0, len(session.tabs))
	for _, tab := range session.tabs {
		tabs = append(tabs, tab)
	}
	session.mu.Unlock()
	if !eligible {
		return
	}
	for _, tab := range tabs {
		tab.mu.Lock()
		live := tab.durable != nil && !tab.exited && !tab.closed
		tab.mu.Unlock()
		if live {
			return
		}
	}
	session.durableMu.Lock()
	provider := session.durableProvider
	if session.durableGen != generation || session.durableTransport != transport {
		provider = nil
	}
	session.durableMu.Unlock()
	waiting, ok := provider.(updateWaitingProvider)
	if !ok || !waiting.UpdateWaiting() || matchingBackgroundHelper(session, transport, generation) != nil {
		return
	}
	session.mu.Lock()
	if session.helperUpdateRetrying || session.transport != transport || session.generation != generation {
		session.mu.Unlock()
		return
	}
	session.helperUpdateRetrying = true
	baseCtx := session.ctx
	if baseCtx == nil {
		baseCtx = m.ctx
	}
	session.mu.Unlock()
	if !m.startGoroutine(func() { m.runWaitingHelperUpdate(baseCtx, session, transport, generation, provider) }) {
		session.mu.Lock()
		session.helperUpdateRetrying = false
		session.mu.Unlock()
	}
}

func (m *Manager) runWaitingHelperUpdate(baseCtx context.Context, session *Session, transport *transportHandle, generation uint64, previous base.DurableProvider) {
	defer func() {
		session.mu.Lock()
		session.helperUpdateRetrying = false
		session.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(baseCtx, helperUpdateRetryTimeout)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	resolveCtx := ipc.WithHelperProgressScope(ctx, ipc.HelperProgressScope{SessionID: session.ID, AssetID: session.Asset().ID})
	m.logger.Debug("retrying the deferred helper update after the last durable terminal ended", "session", session.ID)
	provider, err := m.resolver.ResolveDurable(resolveCtx, transport.Transport)
	if err != nil || provider == nil {
		m.logger.Debug("deferred helper update retry did not finish; the current helper stays in use", "session", session.ID, "error", err)
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.transport != transport || session.generation != generation {
		return
	}
	session.durableMu.Lock()
	defer session.durableMu.Unlock()
	if session.durableProvider == previous && session.durableGen == generation && session.durableTransport == transport {
		session.durableProvider = provider
	}
}
