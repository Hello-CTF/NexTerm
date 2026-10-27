package session

import (
	"context"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/terminal/shellintegr"
)

// KindRemote marks a remote session shell: no local process, no transport; output is pushed in by the feeder (desktop)
// via FeedRemote, and read-only subscriptions (Spectator/preview) take the stream via the same path as real terminal tabs.
const KindRemote = "remote"

// MaxRemoteSessionsPerUser caps concurrently live remote session shells per account, preventing shells from being
// created but never reaped until memory runs out; explicit end (CloseRemote) or idle reaping frees the slot.
const MaxRemoteSessionsPerUser = 32

var (
	ErrRemoteLimit        = errors.New("remote session limit reached")
	ErrRemoteFeedAttached = errors.New("remote session already has an active feed")
)

// RemoteSessionOptions holds the inputs for registering a remote session shell: OwnerID is the owning account (ownership checks share
// TabOwner with preview creation); Title/AssetID are metadata only (for display) and do not participate in byAsset
// dedup - shells skip Connect, so asset.ID is never registered in m.byAsset, avoiding conflicts with real assets.
type RemoteSessionOptions struct {
	OwnerID string
	Title   string
	AssetID string
	Cols    uint32
	Rows    uint32
}

// RegisterRemoteSession registers a terminal tab with no local process in the session hub: the in-memory terminal
// model is the output buffer and snapshot source, and output pushed in by FeedRemote later both updates the screen
// (the Spectator first-frame snapshot) and broadcasts to subscribers via the existing feed path. The shell's session
// status is always connected, and its lifecycle ends via EndRemote/CloseRemote or idle reaping.
func (m *Manager) RegisterRemoteSession(ctx context.Context, options RemoteSessionOptions) (TabInfo, error) {
	if options.OwnerID == "" {
		return TabInfo{}, errors.New("remote session owner is empty")
	}
	cols, rows := options.Cols, options.Rows
	if cols == 0 && rows == 0 {
		cols, rows = 80, 24
	}
	if !validSize(cols, rows) {
		return TabInfo{}, ErrInvalidSize
	}
	if err := ctx.Err(); err != nil {
		return TabInfo{}, err
	}

	now := m.now()
	sessionCtx, sessionCancel := context.WithCancel(ownerCtx(m.ctx, options.OwnerID))
	session := &Session{
		ID:          newID(),
		CreatedAt:   now,
		asset:       Asset{ID: options.AssetID, Name: options.Title, Kind: KindRemote},
		userID:      options.OwnerID,
		status:      StatusConnected,
		generation:  1,
		ctx:         sessionCtx,
		cancel:      sessionCancel,
		tabs:        make(map[string]*Tab),
		connectDone: make(chan struct{}),
	}
	if session.asset.ID == "" {
		session.asset.ID = "remote/" + session.ID
	}
	session.finishConnectLocked(nil)
	session.remoteActivity = now

	tabID := newID()
	terminal, err := m.terminals.NewTerminal(TerminalConfig{TabID: tabID, SessionID: session.ID, Cols: cols, Rows: rows})
	if err != nil {
		sessionCancel()
		return TabInfo{}, err
	}

	tabCtx, tabCancel := context.WithCancel(m.ctx)
	tab := &Tab{
		ID: tabID, SessionID: session.ID, session: session, terminal: terminal, fanout: m.newOutputFanout(),
		cols: cols, rows: rows, encoding: canonicalEncoding(""),
		generation: 1, subscribers: make(map[string]subscriber), streamEpoch: ids.New(), outputWake: make(chan struct{}),
		ctx: tabCtx, cancel: tabCancel, responses: newResponseQueue(1), feedGate: make(chan struct{}, 1),
		writeGate:  make(chan struct{}, 1),
		cwdTracker: shellintegr.NewTracker(), commandTrack: shellintegr.NewCommandTracker(),
		runtimeEpisode: ids.New(),
	}
	tab.createdAt = time.Now()
	tab.detachedAt = tab.createdAt
	tab.feedGate <- struct{}{}
	// The shell has no transport, so the terminal model's responses to query sequences (e.g. DA) have nowhere to go and are dropped;
	// the feeder's local terminal already answered them on its own side.
	terminal.SetResponseHandler(func([]byte) {})

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		tabCancel()
		sessionCancel()
		terminal.Close()
		return TabInfo{}, ErrSessionClosed
	}
	owned := 0
	for _, existing := range m.sessions {
		if existing.asset.Kind == KindRemote && existing.userID == options.OwnerID {
			owned++
		}
	}
	if owned >= MaxRemoteSessionsPerUser {
		m.mu.Unlock()
		tabCancel()
		sessionCancel()
		terminal.Close()
		return TabInfo{}, ErrRemoteLimit
	}
	m.sessions[session.ID] = session
	m.nextTabIncarnation++
	tab.incarnation = m.nextTabIncarnation
	m.tabs[tab.ID] = tab
	session.tabs[tab.ID] = tab
	session.mu.Lock()
	connectedEvent := session.statusEventLocked(StatusConnected, nil)
	session.mu.Unlock()
	tab.mu.Lock()
	controlEvent := tab.controlEventLocked()
	info := tab.infoLocked()
	tab.mu.Unlock()
	m.mu.Unlock()

	m.emit(ctx, TopicSessionStatus, connectedEvent)
	m.emit(ctx, TopicTerminalControl, controlEvent)
	m.transcriptStarted(ctx, session)
	return info, nil
}

// remoteTab fetches the tab and verifies it is a live remote shell; later operations share this entry point,
// ensuring normal terminal tabs are never mistakenly written through remote channels.
func (m *Manager) remoteTab(tabID string) (*Tab, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return nil, err
	}
	tab.mu.Lock()
	defer tab.mu.Unlock()
	if tab.session.asset.Kind != KindRemote {
		return nil, ErrUnsupported
	}
	if tab.closed || tab.destroying {
		return nil, ErrTabClosed
	}
	return tab, nil
}

func (s *Session) touchRemoteActivity(now time.Time) {
	s.mu.Lock()
	s.remoteActivity = now
	s.mu.Unlock()
}

// remoteWatchingLocked reports whether the shell has subscribed watchers (Spectator/preview viewers);
// the caller must hold session.mu, and the tab.mu acquisition order matches detachChannelsLocked.
func (s *Session) remoteWatchingLocked() bool {
	for _, tab := range s.tabs {
		tab.mu.Lock()
		watching := len(tab.subscribers) > 0
		tab.mu.Unlock()
		if watching {
			return true
		}
	}
	return false
}

// RemoteSessionInfo returns the remote shell's tab info; non-remote shells return ErrUnsupported.
func (m *Manager) RemoteSessionInfo(tabID string) (TabInfo, error) {
	tab, err := m.remoteTab(tabID)
	if err != nil {
		return TabInfo{}, err
	}
	return tab.Info(), nil
}

// FeedRemote pushes one frame of remote output from the feeder into the shell: it updates the screen model and broadcasts to all subscribers
// (Spectators receive binary output frames identical to a real terminal). Pushes are rejected after end.
func (m *Manager) FeedRemote(ctx context.Context, tabID string, data []byte) error {
	tab, err := m.remoteTab(tabID)
	if err != nil {
		return err
	}
	tab.mu.Lock()
	exited := tab.exited
	generation := tab.generation
	tab.mu.Unlock()
	if exited {
		return ErrTabClosed
	}
	if err := m.feed(ctx, tab, generation, data); err != nil {
		return err
	}
	tab.session.touchRemoteActivity(m.now())
	return nil
}

// EndRemote marks the remote shell as ended (the feeder's terminal has exited): same semantics as a real process exit - the tab is
// kept and the snapshot stays readable by new subscribers, but no new output is accepted. Repeated calls are an idempotent no-op.
func (m *Manager) EndRemote(tabID string, exitCode *int) error {
	tab, err := m.remoteTab(tabID)
	if err != nil {
		return err
	}
	tab.mu.Lock()
	if tab.exited {
		tab.mu.Unlock()
		return nil
	}
	tab.exited = true
	exitEvent := tab.exitEventLocked(exitCode)
	controlEvent := tab.controlEventLocked()
	tab.mu.Unlock()
	tab.session.touchRemoteActivity(m.now())
	m.emit(context.Background(), TopicTerminalExit, exitEvent)
	m.emit(context.Background(), TopicTerminalControl, controlEvent)
	return nil
}

// ResizeRemote adjusts the remote shell's screen size (synced when the feeder terminal resizes), and the snapshot reflects the
// new size; unlike interactive Resize, the shell has no grid negotiation and the model is changed directly.
func (m *Manager) ResizeRemote(ctx context.Context, tabID string, cols, rows uint32) error {
	if !validSize(cols, rows) {
		return ErrInvalidSize
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tab, err := m.remoteTab(tabID)
	if err != nil {
		return err
	}
	if err := tab.terminal.Resize(int(cols), int(rows)); err != nil {
		return err
	}
	tab.mu.Lock()
	tab.cols = cols
	tab.rows = rows
	controlEvent := tab.controlEventLocked()
	tab.mu.Unlock()
	tab.session.touchRemoteActivity(m.now())
	m.emit(ctx, TopicTerminalControl, controlEvent)
	m.transcriptResize(ctx, tab, cols, rows)
	return nil
}

// AttachRemoteFeed occupies the shell's feed slot: only one feed connection is allowed per shell at a time, preventing two
// streams from interleaving and corrupting the screen; idle reaping is suspended while occupied (the connection itself is activity).
// It returns the shell's owning session for DetachRemoteFeed to release in pairs.
func (m *Manager) AttachRemoteFeed(tabID string) (*Session, error) {
	tab, err := m.remoteTab(tabID)
	if err != nil {
		return nil, err
	}
	session := tab.session
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil, ErrSessionClosed
	}
	tab.mu.Lock()
	exited := tab.exited
	tab.mu.Unlock()
	if exited {
		return nil, ErrTabClosed
	}
	if session.remoteFeeds > 0 {
		return nil, ErrRemoteFeedAttached
	}
	session.remoteFeeds++
	session.remoteActivity = m.now()
	return session, nil
}

// DetachRemoteFeed releases the feed slot and resets the idle timer to the disconnect moment: after a disconnect the shell is kept,
// can reattach and resume feeding, until idle timeout reaps it.
func (m *Manager) DetachRemoteFeed(session *Session) {
	if session == nil {
		return
	}
	session.mu.Lock()
	if session.remoteFeeds > 0 {
		session.remoteFeeds--
	}
	session.remoteActivity = m.now()
	session.mu.Unlock()
}

// CloseRemote explicitly ends the remote shell: the whole session with its tabs is removed from the hub and subscriber
// channels close (read-only preview viewers see ended). Unlike EndRemote, the snapshot is not preserved.
func (m *Manager) CloseRemote(tabID string) error {
	tab, err := m.remoteTab(tabID)
	if err != nil {
		return err
	}
	return m.reap(tab.SessionID)
}

// reapRemote is the idle-reap path for remote shells: same semantics as reap, but the candidacy condition is
// "no feed connection and timed out since last activity", rechecked against the activity timestamp at candidacy to avoid mistaken reaping.
func (m *Manager) reapRemote(id string, activity time.Time) error {
	m.mu.Lock()
	session := m.sessions[id]
	if session == nil {
		m.mu.Unlock()
		return nil
	}
	session.mu.Lock()
	if session.asset.Kind != KindRemote || session.closed || session.remoteFeeds != 0 || session.remoteWatchingLocked() || !session.remoteActivity.Equal(activity) {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil
	}
	session.closed = true
	session.status = StatusDisconnected
	session.generation++
	cancel := session.cancel
	session.finishConnectLocked(ErrSessionClosed)
	session.finishReconnectLocked(ErrSessionClosed)
	tabs := make([]*Tab, 0, len(session.tabs))
	for _, tab := range session.tabs {
		tabs = append(tabs, tab)
		delete(m.tabs, tab.ID)
		m.forgetAttention(tab)
		delete(m.throttleVersions, tab.ID)
		delete(m.throttleChannels, tab.ID)
	}
	session.tabs = make(map[string]*Tab)
	delete(m.sessions, id)
	if m.byAsset[session.assetOwnerKey()] == session.ID {
		delete(m.byAsset, session.assetOwnerKey())
	}
	disconnectedEvent := session.statusEventLocked(StatusDisconnected, nil)
	session.mu.Unlock()
	m.mu.Unlock()

	cancel()
	for _, tab := range tabs {
		m.closeTabResources(tab, false)
	}
	m.emit(context.Background(), TopicSessionStatus, disconnectedEvent)
	m.transcriptEnded(session)
	return nil
}
