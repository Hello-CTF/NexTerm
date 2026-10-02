package session

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func (m *Manager) CurrentDialer(ctx context.Context, sessionID string) (base.Dialer, error) {
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
	dialer, ok := session.transport.Transport.(base.Dialer)
	if !ok {
		return nil, ErrUnsupported
	}
	return dialer, nil
}
