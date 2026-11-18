package session

import (
	"context"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// helperUpdateRetryTimeout bounds the background helper update started when a session's last durable terminal ends.
const helperUpdateRetryTimeout = 10 * time.Minute

// helperUpdateRecheckInterval is how often a session whose SSH helper update waits for idle tries it again. The
// update can wait on terminals of other clients or sessions, whose end this session never sees, so the retry when
// its own last durable terminal ends is not enough.
const helperUpdateRecheckInterval = 5 * time.Minute

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

// watchWaitingHelperUpdate starts, at most once per session, the loop that retries a helper update waiting for idle.
// It takes no lock, so it may be called while session locks are held.
func (m *Manager) watchWaitingHelperUpdate(session *Session, provider base.DurableProvider) {
	if m.resolver == nil || session == nil || !providerUpdateWaiting(provider) {
		return
	}
	if !session.helperUpdateRecheck.CompareAndSwap(false, true) {
		return
	}
	if !m.startGoroutine(func() { m.runHelperUpdateRecheck(session) }) {
		session.helperUpdateRecheck.Store(false)
	}
}

func providerUpdateWaiting(provider base.DurableProvider) bool {
	waiting, ok := provider.(updateWaitingProvider)
	return ok && waiting.UpdateWaiting()
}

// runHelperUpdateRecheck retries the waiting helper update every helperUpdateRecheckEvery while the session's provider
// still waits for idle. retryWaitingHelperUpdate keeps its own conditions: no live durable terminal in this session
// and a resolver that checks the whole helper is idle before switching builds.
func (m *Manager) runHelperUpdateRecheck(session *Session) {
	timer := time.NewTimer(m.helperUpdateRecheckEvery)
	defer timer.Stop()
	for {
		select {
		case <-m.ctx.Done():
			session.helperUpdateRecheck.Store(false)
			return
		case <-timer.C:
		}
		session.mu.Lock()
		closed := session.closed
		session.mu.Unlock()
		session.durableMu.Lock()
		provider := session.durableProvider
		session.durableMu.Unlock()
		if closed || !providerUpdateWaiting(provider) {
			session.helperUpdateRecheck.Store(false)
			// A waiting provider stored after the check above found the loop still marked; take it up again.
			session.durableMu.Lock()
			provider = session.durableProvider
			session.durableMu.Unlock()
			if !closed {
				m.watchWaitingHelperUpdate(session, provider)
			}
			return
		}
		m.retryWaitingHelperUpdate(session)
		timer.Reset(m.helperUpdateRecheckEvery)
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
	if session.closed || session.transport != transport || session.generation != generation {
		session.mu.Unlock()
		return
	}
	session.durableMu.Lock()
	replaced := session.durableProvider == previous && session.durableGen == generation && session.durableTransport == transport
	if replaced {
		session.durableProvider = provider
	}
	session.durableMu.Unlock()
	session.mu.Unlock()
	if replaced {
		m.watchWaitingHelperUpdate(session, provider)
	}
}
