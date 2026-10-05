package session

import (
	"context"
	"errors"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

// durableProviderFor resolves the durable provider backing a durable tab on
// the given session. Local sessions use the manager's provider; SSH sessions
// resolve through the configured resolver and cache the result per session
// generation, so a reconnect re-resolves against the new transport.
func (m *Manager) durableProviderFor(ctx context.Context, session *Session, transport *transportHandle, kind string, generation uint64) (base.DurableProvider, error) {
	if kind == KindLocal {
		if m.durable == nil {
			return nil, ErrUnsupported
		}
		return m.durable, nil
	}
	if kind != KindSSH || m.resolver == nil || transport == nil {
		return nil, ErrUnsupported
	}
	session.durableMu.Lock()
	defer session.durableMu.Unlock()
	if session.durableProvider != nil && session.durableGen == generation && session.durableTransport == transport {
		return session.durableProvider, nil
	}
	provider, err := m.resolver.ResolveDurable(ctx, transport.Transport)
	if err != nil {
		return nil, err
	}
	session.durableProvider = provider
	session.durableGen = generation
	session.durableTransport = transport
	return provider, nil
}

// RecoverRemoteDurable searches the durable providers of connected SSH
// sessions for a tab ID and reopens the first match with the given attach
// options. It returns ErrTabNotFound only when the search completes with no
// owning daemon; resolver and enumeration failures are joined and returned
// after exhausting the connected sessions so callers can classify them.
func (m *Manager) RecoverRemoteDurable(ctx context.Context, tabID string, options OpenTabOptions) (TabInfo, error) {
	var searchErrs []error
	for _, info := range m.ListSessions() {
		if info.Kind != KindSSH || info.Status != StatusConnected {
			continue
		}
		session, err := m.Session(info.ID)
		if err != nil {
			continue
		}
		session.mu.Lock()
		transport := session.transport
		generation := session.generation
		session.mu.Unlock()
		provider, err := m.durableProviderFor(ctx, session, transport, info.Kind, generation)
		if err != nil {
			searchErrs = append(searchErrs, err)
			continue
		}
		lister, ok := provider.(DurableLister)
		if !ok {
			continue
		}
		ids, err := lister.ListDurable(ctx)
		if err != nil {
			searchErrs = append(searchErrs, err)
			continue
		}
		if !containsDurableID(ids, tabID) {
			continue
		}
		options.TabID = tabID
		options.SessionID = session.ID
		options.Durable = &DurableTabOptions{Recover: true}
		return m.OpenTab(ctx, options)
	}
	if len(searchErrs) > 0 {
		return TabInfo{}, errors.Join(searchErrs...)
	}
	return TabInfo{}, ErrTabNotFound
}

func containsDurableID(ids []string, id string) bool {
	for _, current := range ids {
		if current == id {
			return true
		}
	}
	return false
}

func (t *Tab) addDurableFed(count int64) {
	t.mu.Lock()
	if t.durable != nil {
		t.durableFed += count
	}
	t.mu.Unlock()
}

// prefixSkipChannel drops the leading bytes of the wrapped channel so a
// reattached durable stream resumes where the previous attachment stopped
// instead of replaying consumed history into the terminal and transcript.
type prefixSkipChannel struct {
	base.Channel
	remaining int64
}

func (c *prefixSkipChannel) Read(p []byte) (int, error) {
	for c.remaining > 0 {
		discard := p
		if int64(len(discard)) > c.remaining {
			discard = discard[:c.remaining]
		}
		count, err := c.Channel.Read(discard)
		c.remaining -= int64(count)
		if err != nil {
			return 0, err
		}
		if count == 0 {
			return 0, nil
		}
	}
	return c.Channel.Read(p)
}
