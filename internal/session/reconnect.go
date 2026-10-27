package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

func (m *Manager) StartReconnect(id string) error {
	return m.startReconnect(id, 0, nil)
}

func (m *Manager) StartReconnectWithAssetFor(ctx context.Context, id string, asset Asset) error {
	if _, err := m.SessionFor(ctx, id); err != nil {
		return err
	}
	return m.startReconnect(id, 0, &asset)
}

func (m *Manager) startReconnect(id string, expectedGeneration uint64, assets ...*Asset) error {
	var refreshed *Asset
	if len(assets) > 0 {
		refreshed = assets[0]
	}
	return m.startReconnectWith(id, expectedGeneration, false, refreshed)
}

// startReconnectImmediate reconnects a connection already known to be dead:
// the first attempt runs without the initial backoff.
func (m *Manager) startReconnectImmediate(id string, expectedGeneration uint64) error {
	return m.startReconnectWith(id, expectedGeneration, true, nil)
}

func (m *Manager) startReconnectWith(id string, expectedGeneration uint64, immediate bool, refreshed *Asset) error {
	if err := m.reconnectPreflight(id, expectedGeneration, refreshed); err != nil {
		return err
	}
	if !m.startGoroutine(func() {
		_ = m.reconnectWith(context.Background(), id, expectedGeneration, immediate)
	}) {
		return ErrSessionClosed
	}
	return nil
}

func (m *Manager) reconnectPreflight(id string, expectedGeneration uint64, refreshed *Asset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[id]
	if session == nil {
		return ErrSessionNotFound
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	asset := session.Asset()
	if refreshed != nil {
		asset = *refreshed
	}
	if !reconnectable(asset.Kind) {
		return ErrUnsupported
	}
	if session.closed {
		return ErrSessionClosed
	}
	if expectedGeneration != 0 && (session.generation != expectedGeneration || session.status != StatusConnected) {
		return ErrStaleGeneration
	}
	if session.reconnecting {
		return nil
	}
	if session.status == StatusConnecting {
		return ErrDisconnected
	}
	if other := m.sessions[m.byAsset[session.assetOwnerKey()]]; other != nil && other != session {
		return fmt.Errorf("%w: %s", ErrAssetSessionConflict, asset.ID)
	}
	if refreshed != nil {
		snapshot := *refreshed
		session.assetSnapshot.Store(&snapshot)
		session.staleConfig = false
	}
	return nil
}

func (m *Manager) MarkSessionConfigStaleFor(ctx context.Context, id string) error {
	requester := userIDFromContext(ctx)
	openAccess := m.requestOpenAccess(ctx)
	if requester == "" && !openAccess {
		return ErrForbidden
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[id]
	if session == nil {
		return ErrSessionNotFound
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !sessionRequestAllowed(session, requester, openAccess) {
		return ErrForbidden
	}
	if !session.closed {
		session.staleConfig = true
	}
	return nil
}

func (m *Manager) Reconnect(ctx context.Context, id string) error {
	return m.reconnect(ctx, id, 0)
}

func (m *Manager) reconnect(ctx context.Context, id string, expectedGeneration uint64) error {
	return m.reconnectWith(ctx, id, expectedGeneration, false)
}

func (m *Manager) reconnectWith(ctx context.Context, id string, expectedGeneration uint64, immediate bool) error {
	m.mu.Lock()
	session := m.sessions[id]
	if session == nil {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	session.mu.Lock()
	if !reconnectable(session.Asset().Kind) {
		session.mu.Unlock()
		m.mu.Unlock()
		return ErrUnsupported
	}
	if !sessionOwnerAllowed(session, userIDFromContext(ctx)) {
		session.mu.Unlock()
		m.mu.Unlock()
		return ErrForbidden
	}
	if session.closed {
		session.mu.Unlock()
		m.mu.Unlock()
		return ErrSessionClosed
	}
	if expectedGeneration != 0 && (session.generation != expectedGeneration || session.status != StatusConnected) {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil
	}
	if session.reconnecting {
		completion := session.reconnectCompletion
		session.mu.Unlock()
		m.mu.Unlock()
		return waitReconnect(ctx, completion)
	}
	if session.status == StatusConnecting {
		session.mu.Unlock()
		m.mu.Unlock()
		return ErrDisconnected
	}
	if other := m.sessions[m.byAsset[session.assetOwnerKey()]]; other != nil && other != session {
		session.mu.Unlock()
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrAssetSessionConflict, session.asset.ID)
	}

	generation := session.generation + 1
	oldCancel := session.cancel
	oldTransport := session.transport
	session.transport = nil
	session.generation = generation
	session.ctx, session.cancel = context.WithCancel(ownerCtx(m.ctx, session.userID))
	session.status = StatusReconnecting
	session.reconnecting = true
	session.reconnectCompletion = &reconnectCompletion{done: make(chan struct{})}
	session.reconnectAttempt = 0
	firstDelay := time.Duration(0)
	if !immediate {
		firstDelay = m.reconnectDelay(0)
	}
	session.reconnectNextRetry = m.now().Add(firstDelay)
	wake := make(chan struct{}, 1)
	session.reconnectWake = wake
	channels := session.detachChannelsLocked()
	m.byAsset[session.assetOwnerKey()] = session.ID
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
	m.transcriptEnded(session)

	runCtx, cancel := context.WithCancel(reconnectCtx)
	stop := context.AfterFunc(ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	return m.reconnectLoop(runCtx, session, generation, firstDelay, wake)
}

// reconnectDelay is the jittered backoff before the given attempt: a uniform
// sample in [d/2, d) of the configured step, so sessions that dropped together
// (a network change, a restarted bastion) do not redial in lockstep.
func (m *Manager) reconnectDelay(attempt int) time.Duration {
	step := m.reconnectBackoff[min(attempt, len(m.reconnectBackoff)-1)]
	if step <= 0 {
		return 0
	}
	half := step / 2
	return half + time.Duration(m.jitter()*float64(step-half))
}

// waitReconnectBackoff sleeps for the backoff unless the context ends or a
// foreground probe wakes the loop. A wake that arrived while the previous
// attempt was still dialing is discarded: that attempt already answered it.
func waitReconnectBackoff(ctx context.Context, delay time.Duration, wake <-chan struct{}) error {
	select {
	case <-wake:
	default:
	}
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wake:
		return nil
	case <-timer.C:
		return nil
	}
}

func waitReconnect(ctx context.Context, completion *reconnectCompletion) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-completion.done:
	}
	// Closing done publishes this attempt's immutable result even if another
	// reconnect has already replaced the session's current completion.
	return completion.err
}

func (m *Manager) reconnectLoop(ctx context.Context, session *Session, generation uint64, delay time.Duration, wake <-chan struct{}) error {
	var lastErr error
	for attempt := 0; attempt < m.reconnectMax; attempt++ {
		if err := waitReconnectBackoff(ctx, delay, wake); err != nil {
			lastErr = err
			break
		}
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
			var gone map[*Tab]error
			// Replacement channels must outlive the reconnect attempt: every
			// transport binds an opened channel's lifetime to the context it
			// receives, so opening with the attempt-scoped ctx tore the fresh
			// channels down moments after Reconnect returned. Open them under
			// a context scoped to the session's lifetime instead: the attempt
			// cancels it only while the opens are in flight, so a blocked open
			// still aborts on caller cancellation, and once the attempt stops
			// watching, a committed channel lives until the session ends.
			session.mu.Lock()
			channelCtx := session.ctx
			session.mu.Unlock()
			openCtx, cancelOpen := context.WithCancel(channelCtx)
			stop := context.AfterFunc(ctx, cancelOpen)
			opened, gone, err = m.openReplacementChannels(openCtx, session, handle, generation)
			stop()
			if err == nil && ctx.Err() != nil {
				err = ctx.Err()
			}
			if err == nil {
				committed, commitErr := m.commitReconnect(session, handle, opened, gone, generation)
				if committed {
					return commitErr
				}
				err = commitErr
			}
			// Every path short of a committed reconnect releases the scoped
			// open context and closes the replacements explicitly.
			cancelOpen()
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
		if !m.noteReconnectAttempt(session, generation, attempt+1) {
			return lastErr
		}
		if isNonRetryableReconnectError(lastErr) {
			break
		}
		if attempt+1 < m.reconnectMax {
			delay = m.reconnectDelay(attempt + 1)
			event, current := m.reconnectRetryEvent(session, generation, lastErr, m.now().Add(delay))
			if !current {
				return lastErr
			}
			m.emit(context.Background(), TopicSessionStatus, event)
		}
	}
	if lastErr == nil {
		lastErr = errors.New("reconnect attempts exhausted")
	}
	return m.finishReconnect(session, generation, lastErr)
}

func isNonRetryableReconnectError(err error) bool {
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		switch appErr.Code {
		case ipc.CodeBadParam, ipc.CodeVaultLocked, ipc.CodeVaultNotInit:
			return true
		}
	}
	var connectErr *ssh.ConnectError
	if errors.As(err, &connectErr) {
		switch connectErr.Kind {
		case ssh.ErrorKindAuth, ssh.ErrorKindHostKeyPending, ssh.ErrorKindHostKeyChanged, ssh.ErrorKindConfig:
			return true
		}
	}
	var hostKeyErr *ssh.HostKeyError
	return errors.As(err, &hostKeyErr)
}

func (m *Manager) noteReconnectAttempt(session *Session, generation uint64, attempt int) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.status != StatusReconnecting || !session.reconnecting || session.generation != generation {
		return false
	}
	session.reconnectAttempt = attempt
	return true
}

func (m *Manager) reconnectRetryEvent(session *Session, generation uint64, cause error, nextRetryAt time.Time) (StatusEvent, bool) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.status != StatusReconnecting || !session.reconnecting || session.generation != generation {
		return StatusEvent{}, false
	}
	session.reconnectNextRetry = nextRetryAt
	return session.statusEventLocked(StatusReconnecting, cause), true
}

func (m *Manager) openReplacementChannels(ctx context.Context, session *Session, transport *transportHandle, generation uint64) (map[*Tab]*replacementChannel, map[*Tab]error, error) {
	opened := make(map[*Tab]*replacementChannel)
	gone := make(map[*Tab]error)
	if !ptyBacked(session.Asset().Kind) {
		return opened, gone, nil
	}
	session.mu.Lock()
	tabs := make([]*Tab, 0, len(session.tabs))
	for _, tab := range session.tabs {
		tabs = append(tabs, tab)
	}
	kind := session.Asset().Kind
	session.mu.Unlock()
	var provider base.DurableProvider
	for _, tab := range tabs {
		tab.mu.Lock()
		if tab.closed || tab.durableLost {
			tab.mu.Unlock()
			continue
		}
		durableTab := tab.durable != nil
		cols, rows := tab.cols, tab.rows
		durableFed := tab.durableFed
		durableIdentity := tab.durableIdentity
		if tab.grid != nil {
			if desired := tab.grid.Snapshot().DesiredGrid; desired.Valid() {
				cols, rows = uint32(desired.Cols), uint32(desired.Rows)
			}
		}
		tab.mu.Unlock()
		if durableTab {
			if provider == nil {
				var err error
				provider, err = m.durableProviderFor(ctx, session, transport, kind, generation, kind == KindLocal)
				if err != nil {
					return opened, gone, err
				}
			}
			attachment, err := attachDurableReplacement(ctx, provider, tab.ID, durableIdentity, uint64(durableFed))
			if errors.Is(err, durable.ErrNotFound) || errors.Is(err, durable.ErrIdentity) || errors.Is(err, ErrDurableUnrecoverable) || errors.Is(err, durable.ErrGenerationMismatch) {
				if attachment != nil {
					_ = attachment.Close()
				}
				if errors.Is(err, ErrDurableUnrecoverable) || errors.Is(err, durable.ErrGenerationMismatch) {
					m.logger.Warn("durable terminal cannot be recovered after reconnect", "session", session.ID, "tab", tab.ID, "error", err)
				}
				gone[tab] = err
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
				channel: newChannelHandle(attachment),
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

// attachDurableReplacement resumes a durable tab only through checked attach.
// A tab that never recorded an incarnation cannot be matched to its helper
// session, so it is reported as unrecoverable instead of attaching unchecked.
func attachDurableReplacement(ctx context.Context, provider base.DurableProvider, tabID string, identity base.DurableIdentity, offset uint64) (base.DurableAttachment, error) {
	if identity.Incarnation == "" {
		return nil, ErrDurableUnrecoverable
	}
	return provider.AttachFromWithIdentity(ctx, tabID, identity, offset)
}

type replacementChannel struct {
	channel *channelHandle
	durable base.DurableAttachment
}

// reconnectNotice targets a tab that reconnect left exited; generation is the
// tab's own generation because it was not moved to the replacement transport.
type reconnectNotice struct {
	tab        *Tab
	generation uint64
	banner     []byte
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

func (m *Manager) commitReconnect(session *Session, transport *transportHandle, opened map[*Tab]*replacementChannel, gone map[*Tab]error, generation uint64) (bool, error) {
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
	committed := false
	defer func() {
		if !committed {
			for _, event := range controls {
				m.discardControlEvent(event)
			}
		}
	}()
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
			if replacement.durable != nil {
				tab.noteDurableReplacementLocked(replacement.durable)
			}
			if identity, ok := durableAttachmentIdentity(replacement.durable); ok {
				// The checked attach proved the incarnation; record the helper
				// generation it was served by for the next reconnect.
				tab.durableIdentity = identity
			}
			tab.setGenerationLocked(generation)
			tab.exited = false
			if tab.runtimeEpisode != "" {
				// The replacement channel is a new runtime: advance the
				// episode so a roster row bound to the pre-reconnect
				// episode cannot keep reporting connected or resolve the
				// replacement asset, and reset the cwd evidence with it —
				// the old runtime's reports never describe the replacement.
				// Durable tabs carry no runtimeEpisode; their daemon
				// incarnation stays stable across recovery.
				tab.advanceRuntimeEpisodeLocked()
			}
			cols, rows := reconnectGeometryLocked(tab)
			controls = append(controls, tab.controlEventLocked())
			start = append(start, tab)
			resizes = append(resizes, reconnectResize{tab: tab, cols: cols, rows: rows})
			delete(opened, tab)
		}
		tab.mu.Unlock()
	}
	var unrecoverable []reconnectNotice
	for tab, cause := range gone {
		tab.mu.Lock()
		if !tab.closed && m.tabs[tab.ID] == tab && session.tabs[tab.ID] == tab {
			switch {
			case errors.Is(cause, ErrDurableUnrecoverable):
				unrecoverable = append(unrecoverable, reconnectNotice{tab: tab, generation: tab.generation, banner: durableUnrecoverableBanner})
			case errors.Is(cause, durable.ErrGenerationMismatch):
				unrecoverable = append(unrecoverable, reconnectNotice{tab: tab, generation: tab.generation, banner: durableHelperRestartedBanner})
			}
			if tab.durable != nil {
				retired = append(retired, tab.durable)
				tab.durable = nil
				goneOffsets = append(goneOffsets, tab.ID)
			}
			tab.durableLost = true
			tab.exited = true
			exits = append(exits, tab.exitEventLocked(nil))
			controls = append(controls, tab.controlEventLocked())
		}
		tab.mu.Unlock()
	}
	if !ptyBacked(session.Asset().Kind) {
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
				if tab.runtimeEpisode != "" {
					// The session transport is the replacement runtime for
					// non-PTY tabs: advance the episode exactly like the
					// channel-replacement branch above, resetting the cwd
					// evidence with it.
					tab.advanceRuntimeEpisodeLocked()
				}
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
		session.idleSince = m.now()
	}
	session.finishReconnectLocked(nil)
	heartbeater := m.armLivenessLocked(session)
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
		m.deleteDurableTranscriptOffset(ownerCtx(context.Background(), session.userID), tabID)
	}
	m.transcriptStarted(reconnectCtx, session)
	for _, resize := range resizes {
		m.transcriptResize(reconnectCtx, resize.tab, resize.cols, resize.rows)
	}
	for _, notice := range unrecoverable {
		_ = m.feed(reconnectCtx, notice.tab, notice.generation, notice.banner)
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
	if ptyBacked(session.Asset().Kind) {
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
	m.startLiveness(session, generation, heartbeater)
	m.emit(context.Background(), TopicSessionStatus, connectedEvent)
	committed = true
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
	if m.byAsset[session.assetOwnerKey()] == session.ID {
		delete(m.byAsset, session.assetOwnerKey())
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
