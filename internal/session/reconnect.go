package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func (m *Manager) StartReconnect(id string) error {
	if err := m.reconnectPreflight(id); err != nil {
		return err
	}
	if !m.startGoroutine(func() {
		_ = m.Reconnect(context.Background(), id)
	}) {
		return ErrSessionClosed
	}
	return nil
}

func (m *Manager) reconnectPreflight(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[id]
	if session == nil {
		return ErrSessionNotFound
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !reconnectable(session.asset.Kind) {
		return ErrUnsupported
	}
	if session.closed {
		return ErrSessionClosed
	}
	if session.reconnecting {
		return nil
	}
	if session.status == StatusConnecting {
		return ErrDisconnected
	}
	if other := m.sessions[m.byAsset[session.asset.ID]]; other != nil && other != session {
		return fmt.Errorf("%w: %s", ErrAssetSessionConflict, session.asset.ID)
	}
	return nil
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
		return fmt.Errorf("%w: %s", ErrAssetSessionConflict, session.asset.ID)
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

func (m *Manager) openReplacementChannels(ctx context.Context, session *Session, transport *transportHandle, generation uint64) (map[*Tab]*replacementChannel, map[*Tab]struct{}, error) {
	opened := make(map[*Tab]*replacementChannel)
	gone := make(map[*Tab]struct{})
	if !ptyBacked(session.asset.Kind) {
		return opened, gone, nil
	}
	session.mu.Lock()
	tabs := make([]*Tab, 0, len(session.tabs))
	for _, tab := range session.tabs {
		tabs = append(tabs, tab)
	}
	kind := session.asset.Kind
	session.mu.Unlock()
	var provider base.DurableProvider
	for _, tab := range tabs {
		tab.mu.Lock()
		if tab.closed {
			tab.mu.Unlock()
			continue
		}
		durableTab := tab.durable != nil
		cols, rows := tab.cols, tab.rows
		durableFed := tab.durableFed
		if tab.grid != nil {
			if desired := tab.grid.Snapshot().DesiredGrid; desired.Valid() {
				cols, rows = uint32(desired.Cols), uint32(desired.Rows)
			}
		}
		tab.mu.Unlock()
		if durableTab {
			if provider == nil {
				var err error
				provider, err = m.durableProviderFor(ctx, session, transport, kind, generation)
				if err != nil {
					return opened, gone, err
				}
			}
			attachment, err := provider.Attach(ctx, tab.ID)
			if errors.Is(err, durable.ErrNotFound) {
				gone[tab] = struct{}{}
				continue
			}
			if attachment == nil {
				if err == nil {
					err = errors.New("durable provider returned a nil attachment")
				}
				return opened, gone, err
			}
			if err != nil {
				_ = attachment.Close()
				return opened, gone, err
			}
			opened[tab] = &replacementChannel{
				channel: newChannelHandle(&prefixSkipChannel{Channel: attachment, remaining: durableFed}),
				durable: attachment,
			}
			continue
		}
		ptyTransport, ok := transport.Transport.(base.PTYTransport)
		if !ok {
			return opened, gone, ErrUnsupported
		}
		channel, err := ptyTransport.OpenPTY(ctx, base.PTYOptions{
			Cols: cols, Rows: rows, Term: "xterm-256color", ExpectedGeneration: transport.Generation(),
		})
		if err != nil {
			if channel != nil {
				_ = channel.Close()
			}
			return opened, gone, err
		}
		if channel == nil {
			return opened, gone, errors.New("transport returned a nil PTY")
		}
		opened[tab] = &replacementChannel{channel: newChannelHandle(channel)}
	}
	return opened, gone, nil
}

type replacementChannel struct {
	channel *channelHandle
	durable base.DurableAttachment
}

type reconnectResize struct {
	tab  *Tab
	cols uint32
	rows uint32
}

// reconnectGeometryLocked returns the tab's effective terminal geometry,
// preferring the grid's desired size over the last applied size. Callers must
// hold tab.mu.
func reconnectGeometryLocked(tab *Tab) (cols, rows uint32) {
	cols, rows = tab.cols, tab.rows
	if tab.grid != nil {
		if desired := tab.grid.Snapshot().DesiredGrid; desired.Valid() {
			cols, rows = uint32(desired.Cols), uint32(desired.Rows)
		}
	}
	return cols, rows
}

func (m *Manager) commitReconnect(session *Session, transport *transportHandle, opened map[*Tab]*replacementChannel, gone map[*Tab]struct{}, generation uint64) (bool, error) {
	m.mu.Lock()
	session.mu.Lock()
	if m.closed || session.closed || m.sessions[session.ID] != session || session.status != StatusReconnecting || !session.reconnecting || session.generation != generation {
		session.mu.Unlock()
		m.mu.Unlock()
		return false, ErrStaleGeneration
	}
	start := make([]*Tab, 0, len(opened))
	controls := make([]ControlEvent, 0, len(opened)+len(gone))
	exits := make([]ExitEvent, 0, len(gone))
	resizes := make([]reconnectResize, 0, len(opened))
	var retired []base.DurableAttachment
	var goneOffsets []string
	for tab, replacement := range opened {
		tab.mu.Lock()
		if !tab.closed && m.tabs[tab.ID] == tab && session.tabs[tab.ID] == tab {
			if tab.grid != nil {
				if err := tab.grid.Reattach(m.gridResize(tab, replacement.channel, generation)); err != nil {
					tab.mu.Unlock()
					session.mu.Unlock()
					m.mu.Unlock()
					return false, err
				}
			}
			if tab.durable != nil {
				retired = append(retired, tab.durable)
			}
			tab.channel = replacement.channel
			tab.durable = replacement.durable
			tab.setGenerationLocked(generation)
			tab.exited = false
			cols, rows := reconnectGeometryLocked(tab)
			controls = append(controls, tab.controlEventLocked())
			start = append(start, tab)
			resizes = append(resizes, reconnectResize{tab: tab, cols: cols, rows: rows})
			delete(opened, tab)
		}
		tab.mu.Unlock()
	}
	for tab := range gone {
		tab.mu.Lock()
		if !tab.closed && m.tabs[tab.ID] == tab && session.tabs[tab.ID] == tab {
			if tab.durable != nil {
				retired = append(retired, tab.durable)
				tab.durable = nil
				goneOffsets = append(goneOffsets, tab.ID)
			}
			tab.exited = true
			exits = append(exits, tab.exitEventLocked(nil))
			controls = append(controls, tab.controlEventLocked())
		}
		tab.mu.Unlock()
	}
	if !ptyBacked(session.asset.Kind) {
		for _, tab := range session.tabs {
			tab.mu.Lock()
			if !tab.closed && m.tabs[tab.ID] == tab {
				if tab.grid != nil {
					if err := tab.grid.Reattach(m.gridResize(tab, nil, generation)); err != nil {
						tab.mu.Unlock()
						session.mu.Unlock()
						m.mu.Unlock()
						return false, err
					}
				}
				tab.exited = false
				cols, rows := reconnectGeometryLocked(tab)
				controls = append(controls, tab.controlEventLocked())
				resizes = append(resizes, reconnectResize{tab: tab, cols: cols, rows: rows})
			}
			tab.mu.Unlock()
		}
	}
	session.transport = transport
	session.status = StatusConnected
	if len(session.tabs) == 0 {
		session.idleSince = time.Now()
	}
	session.finishReconnectLocked(nil)
	reconnectCtx := session.ctx
	connectedEvent := session.statusEventLocked(StatusConnected, nil)
	session.mu.Unlock()
	m.mu.Unlock()
	for _, replacement := range opened {
		_ = replacement.channel.Close()
	}
	for _, attachment := range retired {
		_ = attachment.Close()
	}
	for _, tabID := range goneOffsets {
		m.deleteDurableTranscriptOffset(tabID)
	}
	m.transcriptStarted(reconnectCtx, session)
	for _, resize := range resizes {
		m.transcriptResize(reconnectCtx, resize.tab, resize.cols, resize.rows)
	}
	for _, exit := range exits {
		m.emit(context.Background(), TopicTerminalExit, exit)
	}
	for _, control := range controls {
		m.emit(context.Background(), TopicTerminalControl, control)
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
	m.emit(context.Background(), TopicSessionStatus, connectedEvent)
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
	finishedEvent := session.statusEventLocked(status, reconnectErr)
	session.mu.Unlock()
	m.mu.Unlock()
	if status == StatusFailed && !errors.Is(reconnectErr, context.Canceled) {
		m.logger.Warn("session reconnect failed", "session", session.ID, "error", reconnectErr)
	}
	m.emit(context.Background(), TopicSessionStatus, finishedEvent)
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
