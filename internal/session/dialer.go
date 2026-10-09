package session

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func (m *Manager) Transport(ctx context.Context, sessionID string) (base.Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil {
		return nil, ErrSessionNotFound
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.status != StatusConnected || session.transport == nil {
		return nil, ErrDisconnected
	}
	return session.transport.Transport, nil
}

func (m *Manager) CurrentDialer(ctx context.Context, sessionID string) (base.Dialer, error) {
	transport, err := m.Transport(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	dialer, ok := transport.(base.Dialer)
	if !ok {
		return nil, ErrUnsupported
	}
	return dialer, nil
}
