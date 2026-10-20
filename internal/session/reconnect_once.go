package session

import (
	"context"
	"errors"
	"fmt"
)

// ReconnectOnceFor synchronously reconnects the session with the refreshed
// asset in a single attempt and returns the actual dial error. The caller's
// context controls the attempt: cancellation or deadline stops the dial, and
// nothing continues in the background afterwards. It backs the R-07 broadcast
// flow, where a dropped session may be reconnected at most once and the caller
// must observe the real failure; the interactive auto-reconnect budget is
// unchanged. Like every other request-scoped session entry point, it
// authorizes the caller first: a rejected caller leaves the session, its asset
// snapshot, and its transport untouched and never dials.
func (m *Manager) ReconnectOnceFor(ctx context.Context, id string, asset Asset) error {
	if _, err := m.SessionFor(ctx, id); err != nil {
		return err
	}
	if err := m.reconnectPreflight(id, 0, &asset); err != nil {
		return err
	}
	session, generation, err := m.beginReconnectOnce(ctx, id)
	if err != nil {
		return err
	}
	return m.reconnectOnce(ctx, session, generation)
}

// beginReconnectOnce moves the session into the reconnecting state exactly
// like the interactive reconnect path, returning the session and its new
// generation for the single attempt.
func (m *Manager) beginReconnectOnce(ctx context.Context, id string) (*Session, uint64, error) {
	m.mu.Lock()
	session := m.sessions[id]
	if session == nil {
		m.mu.Unlock()
		return nil, 0, ErrSessionNotFound
	}
	session.mu.Lock()
	if !reconnectable(session.Asset().Kind) {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil, 0, ErrUnsupported
	}
	if !sessionOwnerAllowed(session, userIDFromContext(ctx)) {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil, 0, ErrForbidden
	}
	if session.closed {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil, 0, ErrSessionClosed
	}
	if session.reconnecting {
		done := session.reconnectDone
		session.mu.Unlock()
		m.mu.Unlock()
		return nil, 0, waitReconnect(ctx, session, done)
	}
	if session.status == StatusConnecting {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil, 0, ErrDisconnected
	}
	if other := m.sessions[m.byAsset[session.assetOwnerKey()]]; other != nil && other != session {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil, 0, fmt.Errorf("%w: %s", ErrAssetSessionConflict, session.asset.ID)
	}

	generation := session.generation + 1
	oldCancel := session.cancel
	oldTransport := session.transport
	session.transport = nil
	session.generation = generation
	session.ctx, session.cancel = context.WithCancel(m.ctx)
	session.status = StatusReconnecting
	session.reconnecting = true
	session.reconnectDone = make(chan struct{})
	session.reconnectErr = nil
	session.reconnectAttempt = 0
	session.reconnectNextRetry = m.now().Add(m.reconnectBackoff[0])
	channels := session.detachChannelsLocked()
	m.byAsset[session.assetOwnerKey()] = session.ID
	reconnectingEvent := session.statusEventLocked(StatusReconnecting, nil)
	session.mu.Unlock()
	m.mu.Unlock()

	oldCancel()
	closeChannels(channels)
	if oldTransport != nil {
		_ = oldTransport.Close()
	}
	m.emit(context.Background(), TopicSessionStatus, reconnectingEvent)
	m.transcriptEnded(id)
	return session, generation, nil
}

// reconnectOnce performs the single dial with the caller's context and
// finishes the reconnect with the actual error; it never retries.
func (m *Manager) reconnectOnce(ctx context.Context, session *Session, generation uint64) error {
	transport, err := m.connector.Connect(ctx, session.Asset(), generation)
	var handle *transportHandle
	if transport != nil {
		handle = newTransportHandle(transport)
	}
	if err == nil && handle == nil {
		err = errors.New("connector returned a nil transport")
	}
	if err == nil && !m.reconnectCurrent(session, generation) {
		err = ErrStaleGeneration
	}
	if err == nil {
		var opened map[*Tab]*replacementChannel
		var gone map[*Tab]struct{}
		opened, gone, err = m.openReplacementChannels(ctx, session, handle, generation)
		if err == nil {
			committed, commitErr := m.commitReconnect(session, handle, opened, gone, generation)
			if committed {
				return commitErr
			}
			err = commitErr
		}
		for _, replacement := range opened {
			_ = replacement.channel.Close()
		}
	}
	if handle != nil {
		_ = handle.Close()
	}
	m.noteReconnectAttempt(session, generation, 1)
	return m.finishReconnect(session, generation, err)
}
