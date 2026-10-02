package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func (m *Manager) StartReconnect(id string) bool {
	return m.startGoroutine(func() {
		_ = m.Reconnect(context.Background(), id)
	})
}

func (m *Manager) Reconnect(ctx context.Context, id string) error {
	m.mu.Lock()
	session := m.sessions[id]
	if session == nil {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	session.mu.Lock()
	if !reconnectable(session.asset.Kind) {
		session.mu.Unlock()
		m.mu.Unlock()
		return ErrUnsupported
	}
	if session.closed {
		session.mu.Unlock()
		m.mu.Unlock()
		return ErrSessionClosed
	}
	if session.reconnecting {
		done := session.reconnectDone
		session.mu.Unlock()
		m.mu.Unlock()
		return waitReconnect(ctx, session, done)
	}
	if session.status == StatusConnecting {
		session.mu.Unlock()
		m.mu.Unlock()
		return ErrDisconnected
	}
	if other := m.sessions[m.byAsset[session.asset.ID]]; other != nil && other != session {
		session.mu.Unlock()
		m.mu.Unlock()
		return fmt.Errorf("asset %s already has an active session", session.asset.ID)
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
	channels := session.detachChannelsLocked()
	m.byAsset[session.asset.ID] = session.ID
	reconnectCtx := session.ctx
	session.mu.Unlock()
	m.mu.Unlock()

	oldCancel()
	closeChannels(channels)
	if oldTransport != nil {
		_ = oldTransport.Close()
	}
	m.emit(context.Background(), TopicSessionStatus, StatusEvent{SessionID: id, Status: StatusReconnecting})

	runCtx, cancel := context.WithCancel(reconnectCtx)
	stop := context.AfterFunc(ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	return m.reconnectLoop(runCtx, session, generation)
}

func waitReconnect(ctx context.Context, session *Session, done <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.status == StatusConnected {
		return nil
	}
	if session.reconnectErr != nil {
		return session.reconnectErr
	}
	return ErrDisconnected
}

func (m *Manager) reconnectLoop(ctx context.Context, session *Session, generation uint64) error {
	var lastErr error
	for attempt := 0; attempt < m.reconnectMax; attempt++ {
		backoff := m.reconnectBackoff[min(attempt, len(m.reconnectBackoff)-1)]
		if err := sleepContext(ctx, backoff); err != nil {
			lastErr = err
			break
		}
		transport, err := m.connector.Connect(ctx, session.asset, generation)
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
			var opened map[*Tab]*channelHandle
			opened, err = m.openReplacementChannels(ctx, session, handle, generation)
			if err == nil {
				committed, commitErr := m.commitReconnect(session, handle, opened, generation)
				if committed {
					return commitErr
				}
				err = commitErr
			}
			for _, channel := range opened {
				_ = channel.Close()
			}
		}
		if handle != nil {
			_ = handle.Close()
		}
		lastErr = err
		if !m.reconnectCurrent(session, generation) {
			if lastErr == nil {
				lastErr = ErrStaleGeneration
			}
			return lastErr
		}
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("reconnect attempts exhausted")
	}
	return m.finishReconnect(session, generation, lastErr)
}

func (m *Manager) openReplacementChannels(ctx context.Context, session *Session, transport *transportHandle, generation uint64) (map[*Tab]*channelHandle, error) {
	opened := make(map[*Tab]*channelHandle)
	if !ptyBacked(session.asset.Kind) {
		return opened, nil
	}
	ptyTransport, ok := transport.Transport.(base.PTYTransport)
	if !ok {
		return opened, ErrUnsupported
	}
	session.mu.Lock()
	tabs := make([]*Tab, 0, len(session.tabs))
	for _, tab := range session.tabs {
		tabs = append(tabs, tab)
	}
	session.mu.Unlock()
	for _, tab := range tabs {
		tab.mu.Lock()
		if tab.closed {
			tab.mu.Unlock()
			continue
		}
		cols, rows := tab.cols, tab.rows
		tab.mu.Unlock()
		channel, err := ptyTransport.OpenPTY(ctx, base.PTYOptions{
			Cols: cols, Rows: rows, Term: "xterm-256color", ExpectedGeneration: transport.Generation(),
		})
		if err != nil {
			if channel != nil {
				_ = channel.Close()
			}
			return opened, err
		}
		if channel == nil {
			return opened, errors.New("transport returned a nil PTY")
		}
		opened[tab] = newChannelHandle(channel)
	}
	return opened, nil
}

func (m *Manager) commitReconnect(session *Session, transport *transportHandle, opened map[*Tab]*channelHandle, generation uint64) (bool, error) {
	m.mu.Lock()
	session.mu.Lock()
	if m.closed || session.closed || m.sessions[session.ID] != session || session.status != StatusReconnecting || !session.reconnecting || session.generation != generation {
		session.mu.Unlock()
		m.mu.Unlock()
		return false, ErrStaleGeneration
	}
	start := make([]*Tab, 0, len(opened))
	for tab, channel := range opened {
		tab.mu.Lock()
		if !tab.closed && m.tabs[tab.ID] == tab && session.tabs[tab.ID] == tab {
			tab.channel = channel
			tab.setGenerationLocked(generation)
			tab.exited = false
			start = append(start, tab)
			delete(opened, tab)
		}
		tab.mu.Unlock()
	}
	session.transport = transport
	session.status = StatusConnected
	if len(session.tabs) == 0 {
		session.idleSince = time.Now()
	}
	session.finishReconnectLocked(nil)
	reconnectCtx := session.ctx
	session.mu.Unlock()
	m.mu.Unlock()
	for _, channel := range opened {
		_ = channel.Close()
	}

	for _, tab := range start {
		_ = m.feed(reconnectCtx, tab, generation, reconnectBanner)
	}
	session.mu.Lock()
	tabs := make([]*Tab, 0, len(session.tabs))
	for _, tab := range session.tabs {
		tabs = append(tabs, tab)
	}
	session.mu.Unlock()
	if ptyBacked(session.asset.Kind) {
		for _, tab := range tabs {
			tab.mu.Lock()
			channel := tab.channel
			current := !tab.closed && tab.generation == generation && channel != nil
			tab.mu.Unlock()
			if current {
				m.startPump(tab, channel, generation)
			}
		}
	} else {
		for _, tab := range tabs {
			_ = m.feed(reconnectCtx, tab, generation, reconnectBanner)
		}
	}
	m.emit(context.Background(), TopicSessionStatus, StatusEvent{SessionID: session.ID, Status: StatusConnected})
	return true, nil
}

func (m *Manager) reconnectCurrent(session *Session, generation uint64) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return !session.closed && session.status == StatusReconnecting && session.reconnecting && session.generation == generation
}

func (m *Manager) finishReconnect(session *Session, generation uint64, reconnectErr error) error {
	m.mu.Lock()
	session.mu.Lock()
	if session.closed || session.generation != generation || !session.reconnecting {
		session.mu.Unlock()
		m.mu.Unlock()
		return reconnectErr
	}
	status := StatusFailed
	if session.ctx.Err() != nil {
		status = StatusDisconnected
	}
	session.status = status
	session.finishReconnectLocked(reconnectErr)
	if m.byAsset[session.asset.ID] == session.ID {
		delete(m.byAsset, session.asset.ID)
	}
	session.mu.Unlock()
	m.mu.Unlock()
	m.emit(context.Background(), TopicSessionStatus, StatusEvent{SessionID: session.ID, Status: status, Error: reconnectErr.Error()})
	return reconnectErr
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
