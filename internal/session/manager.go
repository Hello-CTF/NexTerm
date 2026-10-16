package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const defaultSlowConsumerGrace = 30 * time.Second

type channelOwnerRegistration struct {
	ownerID    string
	generation uint64
}

type Manager struct {
	mu                     sync.Mutex
	sessions               map[string]*Session
	byAsset                map[assetOwnerKey]string
	tabs                   map[string]*Tab
	channelTabs            map[string]string
	channelOwners          map[string]channelOwnerRegistration
	channelOwnerGeneration uint64
	sessionOwners          map[string]string
	tabOwners              map[string]string
	controlOwners          map[*recordingControlQueue]string
	throttleVersions       map[string]uint64
	throttleChannels       map[string]map[string]bool
	slowKicks              map[string]*time.Timer
	closed                 bool
	started                bool
	openAccess             bool

	connector   Connector
	terminals   TerminalFactory
	durable     base.DurableProvider
	resolver    DurableResolver
	offsets     DurableTranscriptOffsetStore
	titles      DurableTabTitleStore
	bus         *hub.Hub
	ownsBus     bool
	emitter     Emitter
	transcripts TranscriptSink
	commands    CommandSink
	logger      *slog.Logger

	hookMu        sync.RWMutex
	userInputHook func(string)

	idleTimeout       time.Duration
	sweepInterval     time.Duration
	reconnectMax      int
	reconnectBackoff  []time.Duration
	defaultReplay     int
	slowConsumerGrace time.Duration

	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	closeMu       sync.Mutex
	closeComplete bool
	closeErr      error
	closeDone     chan struct{}
}

func NewManager(config Config) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		sessions:         make(map[string]*Session),
		byAsset:          make(map[assetOwnerKey]string),
		tabs:             make(map[string]*Tab),
		channelTabs:      make(map[string]string),
		channelOwners:    make(map[string]channelOwnerRegistration),
		sessionOwners:    make(map[string]string),
		tabOwners:        make(map[string]string),
		controlOwners:    make(map[*recordingControlQueue]string),
		throttleVersions: make(map[string]uint64),
		throttleChannels: make(map[string]map[string]bool),
		slowKicks:        make(map[string]*time.Timer),
		connector:        config.Connector,
		terminals:        config.Terminals,
		durable:          config.Durable,
		resolver:         config.DurableResolver,
		emitter:          config.Emitter,
		transcripts:      config.Transcripts,
		commands:         config.Commands,
		idleTimeout:      config.IdleTimeout,
		sweepInterval:    config.SweepInterval,
		reconnectMax:     config.ReconnectMax,
		reconnectBackoff: append([]time.Duration(nil), config.ReconnectBackoff...),
		defaultReplay:    config.DefaultReplay,
		openAccess:       config.OpenAccess,
		ctx:              ctx,
		cancel:           cancel,
		closeDone:        make(chan struct{}),
	}
	if manager.terminals == nil {
		manager.terminals = terminalFactory{}
	}
	if manager.idleTimeout == 0 {
		manager.idleTimeout = 30 * time.Minute
	}
	if manager.sweepInterval <= 0 {
		manager.sweepInterval = time.Minute
	}
	if manager.reconnectMax <= 0 {
		manager.reconnectMax = 10
	}
	if len(manager.reconnectBackoff) == 0 {
		manager.reconnectBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	}
	if manager.defaultReplay <= 0 {
		manager.defaultReplay = 4 << 20
	}
	manager.slowConsumerGrace = config.SlowConsumerGrace
	if manager.slowConsumerGrace <= 0 {
		manager.slowConsumerGrace = defaultSlowConsumerGrace
	}
	if config.Hub == nil {
		manager.bus = hub.New(hub.Options{
			OnBackpressure: manager.onBackpressure,
			OnDrain:        manager.onDrain,
		})
		manager.ownsBus = true
	} else {
		manager.bus = config.Hub
	}
	manager.offsets = config.TranscriptOffsets
	if manager.offsets == nil {
		if source, ok := config.Durable.(durableTranscriptOffsetSource); ok {
			manager.offsets = source
		}
	}
	manager.titles = config.TabTitles
	if manager.titles == nil {
		if source, ok := config.Durable.(DurableTabTitleStore); ok {
			manager.titles = source
		}
	}
	manager.logger = config.Logger
	if manager.logger == nil {
		manager.logger = slog.Default()
	}
	if binder, ok := config.Transcripts.(interface {
		BindTranscriptOffsetSource(durableTranscriptOffsetSource)
	}); ok && manager.offsets != nil {
		binder.BindTranscriptOffsetSource(manager.offsets)
	}
	return manager
}

func (m *Manager) Hub() *hub.Hub {
	return m.bus
}

func (m *Manager) Bind(channelID string) (*hub.Receiver, error) {
	return m.bus.Bind(channelID)
}

func (m *Manager) CheckChannelOwner(channelID, ownerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	registration, err := m.channelOwnerLocked(channelID)
	if errors.Is(err, ErrTabNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if registration.ownerID != ownerID {
		return ErrForbidden
	}
	return nil
}

func (m *Manager) RegisterChannelOwner(channelID, ownerID string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	registration, err := m.channelOwnerLocked(channelID)
	if err == nil && registration.ownerID != ownerID {
		return 0, ErrForbidden
	}
	if err != nil && !errors.Is(err, ErrTabNotFound) {
		return 0, err
	}
	m.channelOwnerGeneration++
	generation := m.channelOwnerGeneration
	m.channelOwners[channelID] = channelOwnerRegistration{ownerID: ownerID, generation: generation}
	return generation, nil
}

func (m *Manager) UnregisterChannelOwner(channelID string, generation uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if registration, ok := m.channelOwners[channelID]; ok && registration.generation == generation {
		delete(m.channelOwners, channelID)
	}
}

func (m *Manager) ChannelOwner(channelID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	registration, err := m.channelOwnerLocked(channelID)
	return registration.ownerID, err
}

func (m *Manager) ChannelOwnerGeneration(channelID string) (string, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	registration, err := m.channelOwnerLocked(channelID)
	return registration.ownerID, registration.generation, err
}

func (m *Manager) channelOwnerLocked(channelID string) (channelOwnerRegistration, error) {
	if tab := m.tabs[m.channelTabs[channelID]]; tab != nil && tab.session != nil {
		tab.session.mu.Lock()
		ownerID := tab.session.userID
		tab.session.mu.Unlock()
		return channelOwnerRegistration{ownerID: ownerID}, nil
	}
	if registration, ok := m.channelOwners[channelID]; ok {
		return registration, nil
	}
	return channelOwnerRegistration{}, ErrTabNotFound
}

func (m *Manager) Start(context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrSessionClosed
	}
	if m.started || m.idleTimeout < 0 {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	m.wg.Add(1)
	m.mu.Unlock()
	go m.idleLoop()
	return nil
}

func (m *Manager) Shutdown(context.Context) error {
	return m.Close()
}

func (m *Manager) requestOpenAccess(ctx context.Context) bool {
	return m.openAccess || openAccessFromContext(ctx)
}

func (m *Manager) ConnectFor(ctx context.Context, asset Asset) (*Session, error) {
	if userIDFromContext(ctx) == "" && !m.requestOpenAccess(ctx) {
		return nil, ErrForbidden
	}
	return m.Connect(ctx, asset)
}

func (m *Manager) Connect(ctx context.Context, asset Asset) (*Session, error) {
	if asset.ID == "" {
		return nil, errors.New("asset id is empty")
	}
	if asset.Kind != KindLocal && asset.Kind != KindSSH && asset.Kind != KindDocker && asset.Kind != KindWinRM && asset.Kind != KindDevice {
		return nil, fmt.Errorf("%w: asset kind %s", ErrUnsupported, asset.Kind)
	}
	if m.connector == nil {
		return nil, fmt.Errorf("%w: connector", ErrUnsupported)
	}

	requester := userIDFromContext(ctx)
	assetKey := assetOwnerKey{assetID: asset.ID, ownerScope: requester}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrSessionClosed
	}
	if existing := m.sessions[m.byAsset[assetKey]]; existing != nil {
		existing.mu.Lock()
		allowed := sessionRequestAllowed(existing, requester, true)
		status := existing.status
		done := existing.connectDone
		existing.mu.Unlock()
		m.mu.Unlock()
		// Reusing an existing session skips connector authorization, so every asset kind requires the same account owner.
		if !allowed {
			return nil, ErrForbidden
		}
		if status != StatusConnecting {
			return existing, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
		}
		existing.mu.Lock()
		defer existing.mu.Unlock()
		if existing.status == StatusConnected || existing.status == StatusReconnecting {
			return existing, nil
		}
		if existing.connectErr != nil {
			return nil, existing.connectErr
		}
		return nil, ErrDisconnected
	}

	generation := uint64(1)
	sessionCtx, cancel := context.WithCancel(m.ctx)
	session := &Session{
		ID:          newID(),
		CreatedAt:   time.Now(),
		asset:       asset,
		userID:      requester,
		status:      StatusConnecting,
		generation:  generation,
		ctx:         sessionCtx,
		cancel:      cancel,
		tabs:        make(map[string]*Tab),
		idleSince:   time.Now(),
		connectDone: make(chan struct{}),
	}
	m.sessions[session.ID] = session
	m.byAsset[assetKey] = session.ID
	session.mu.Lock()
	connectingEvent := session.statusEventLocked(StatusConnecting, nil)
	session.mu.Unlock()
	m.mu.Unlock()
	m.emit(ctx, TopicSessionStatus, connectingEvent)

	connectCtx, connectCancel := context.WithCancel(sessionCtx)
	stopCallerCancel := context.AfterFunc(ctx, connectCancel)
	transport, connectErr := m.connector.Connect(connectCtx, asset, generation)
	stopCallerCancel()
	connectCancel()
	var handle *transportHandle
	if transport != nil {
		handle = newTransportHandle(transport)
	} else if connectErr == nil {
		connectErr = errors.New("connector returned a nil transport")
	}

	m.mu.Lock()
	session.mu.Lock()
	current := !m.closed && !session.closed && m.sessions[session.ID] == session && session.generation == generation && session.status == StatusConnecting
	if !current {
		if session.connectErr != nil {
			connectErr = session.connectErr
		} else {
			connectErr = ErrSessionClosed
		}
		session.finishConnectLocked(connectErr)
		session.mu.Unlock()
		m.mu.Unlock()
		if handle != nil {
			_ = handle.Close()
		}
		return nil, connectErr
	}
	if connectErr != nil {
		session.status = StatusFailed
		session.closed = true
		session.finishConnectLocked(connectErr)
		delete(m.sessions, session.ID)
		if m.byAsset[assetKey] == session.ID {
			delete(m.byAsset, assetKey)
		}
		failedEvent := session.statusEventLocked(StatusFailed, connectErr)
		session.mu.Unlock()
		m.mu.Unlock()
		cancel()
		if handle != nil {
			_ = handle.Close()
		}
		m.emit(context.Background(), TopicSessionStatus, failedEvent)
		return nil, connectErr
	}
	session.transport = handle
	session.status = StatusConnected
	session.idleSince = time.Now()
	session.finishConnectLocked(nil)
	connectedEvent := session.statusEventLocked(StatusConnected, nil)
	session.mu.Unlock()
	m.mu.Unlock()
	m.emit(ctx, TopicSessionStatus, connectedEvent)
	m.transcriptStarted(ctx, session)
	return session, nil
}

func (s *Session) finishConnectLocked(err error) {
	if s.connectFinished {
		return
	}
	s.connectFinished = true
	s.connectErr = err
	close(s.connectDone)
}

func (s *Session) finishReconnectLocked(err error) {
	if !s.reconnecting {
		return
	}
	s.reconnecting = false
	s.reconnectErr = err
	close(s.reconnectDone)
}

func (m *Manager) Session(id string) (*Session, error) {
	return m.session(id, "", true)
}

func (m *Manager) SessionFor(ctx context.Context, id string) (*Session, error) {
	requester := userIDFromContext(ctx)
	if requester == "" && !m.requestOpenAccess(ctx) {
		return nil, ErrForbidden
	}
	return m.session(id, requester, m.requestOpenAccess(ctx))
}

func (m *Manager) session(id, requester string, openAccess bool) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[id]
	if session == nil {
		return nil, ErrSessionNotFound
	}
	if requester != "" || !openAccess {
		session.mu.Lock()
		allowed := sessionRequestAllowed(session, requester, openAccess)
		session.mu.Unlock()
		if !allowed {
			return nil, ErrForbidden
		}
	}
	return session, nil
}

func (m *Manager) Tab(id string) (*Tab, error) {
	return m.tab(id, "", true)
}

func (m *Manager) TabFor(ctx context.Context, id string) (*Tab, error) {
	requester := userIDFromContext(ctx)
	if requester == "" && !m.requestOpenAccess(ctx) {
		return nil, ErrForbidden
	}
	return m.tab(id, requester, m.requestOpenAccess(ctx))
}

func (m *Manager) tab(id, requester string, openAccess bool) (*Tab, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tab := m.tabs[id]
	if tab == nil {
		return nil, ErrTabNotFound
	}
	if requester != "" || !openAccess {
		session := tab.session
		if session == nil {
			return nil, ErrForbidden
		}
		session.mu.Lock()
		allowed := sessionRequestAllowed(session, requester, openAccess)
		session.mu.Unlock()
		if !allowed {
			return nil, ErrForbidden
		}
	}
	return tab, nil
}

func (m *Manager) ListSessions() []SessionInfo {
	infos, _ := m.listSessions("", true)
	return infos
}

func (m *Manager) ListSessionsFor(ctx context.Context) ([]SessionInfo, error) {
	return m.listSessions(userIDFromContext(ctx), m.requestOpenAccess(ctx))
}

func (m *Manager) listSessions(requester string, openAccess bool) ([]SessionInfo, error) {
	if requester == "" && !openAccess {
		return nil, ErrForbidden
	}
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		if requester != "" {
			session.mu.Lock()
			allowed := sessionRequestAllowed(session, requester, openAccess)
			session.mu.Unlock()
			if !allowed {
				continue
			}
		}
		sessions = append(sessions, session)
	}
	m.mu.Unlock()
	infos := make([]SessionInfo, 0, len(sessions))
	for _, session := range sessions {
		infos = append(infos, session.Info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos, nil
}

func (m *Manager) ListTabs() []TabInfo {
	infos, _ := m.listTabs("", true)
	return infos
}

func (m *Manager) ListTabsFor(ctx context.Context) ([]TabInfo, error) {
	return m.listTabs(userIDFromContext(ctx), m.requestOpenAccess(ctx))
}

func (m *Manager) listTabs(requester string, openAccess bool) ([]TabInfo, error) {
	if requester == "" && !openAccess {
		return nil, ErrForbidden
	}
	m.mu.Lock()
	tabs := make([]*Tab, 0, len(m.tabs))
	for _, tab := range m.tabs {
		if requester != "" {
			session := tab.session
			if session == nil {
				continue
			}
			session.mu.Lock()
			allowed := sessionRequestAllowed(session, requester, openAccess)
			session.mu.Unlock()
			if !allowed {
				continue
			}
		}
		tabs = append(tabs, tab)
	}
	m.mu.Unlock()
	infos := make([]TabInfo, 0, len(tabs))
	for _, tab := range tabs {
		infos = append(infos, tab.Info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos, nil
}

func (m *Manager) DisconnectFor(ctx context.Context, id string) error {
	if _, err := m.SessionFor(ctx, id); err != nil {
		return err
	}
	return m.Disconnect(id)
}

func (m *Manager) StartReconnectFor(ctx context.Context, id string) error {
	if _, err := m.SessionFor(ctx, id); err != nil {
		return err
	}
	return m.StartReconnect(id)
}

func (m *Manager) ReconnectFor(ctx context.Context, id string) error {
	if _, err := m.SessionFor(ctx, id); err != nil {
		return err
	}
	return m.Reconnect(ctx, id)
}

func (m *Manager) TransportFor(ctx context.Context, id string) (base.Transport, error) {
	if _, err := m.SessionFor(ctx, id); err != nil {
		return nil, err
	}
	return m.Transport(ctx, id)
}

func (m *Manager) CurrentDialerFor(ctx context.Context, id string) (base.Dialer, error) {
	if _, err := m.SessionFor(ctx, id); err != nil {
		return nil, err
	}
	return m.CurrentDialer(ctx, id)
}

func (m *Manager) Disconnect(id string) error {
	return m.disconnect(id, nil)
}

func (m *Manager) disconnect(id string, expectedIdle *time.Time) error {
	m.mu.Lock()
	session := m.sessions[id]
	if session == nil {
		m.mu.Unlock()
		return nil
	}
	if !reconnectable(session.asset.Kind) {
		m.mu.Unlock()
		if expectedIdle == nil {
			return m.reap(id)
		}
		return m.reap(id, *expectedIdle)
	}
	session.mu.Lock()
	if expectedIdle != nil && (session.status != StatusConnected || len(session.tabs) != 0 || !session.idleSince.Equal(*expectedIdle)) {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil
	}
	if session.closed || session.status == StatusDisconnected {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil
	}
	session.status = StatusDisconnected
	session.closed = false
	session.generation++
	cancel := session.cancel
	transport := session.transport
	session.transport = nil
	session.finishConnectLocked(ErrDisconnected)
	session.finishReconnectLocked(ErrDisconnected)
	channels := session.detachChannelsLocked()
	if m.byAsset[session.assetOwnerKey()] == session.ID {
		delete(m.byAsset, session.assetOwnerKey())
	}
	disconnectedEvent := session.statusEventLocked(StatusDisconnected, nil)
	session.mu.Unlock()
	m.mu.Unlock()

	cancel()
	closeChannels(channels)
	if transport != nil {
		_ = transport.Close()
	}
	m.emit(context.Background(), TopicSessionStatus, disconnectedEvent)
	m.transcriptEnded(id)
	return nil
}

func (s *Session) detachChannelsLocked() []*channelHandle {
	channels := make([]*channelHandle, 0, len(s.tabs))
	for _, tab := range s.tabs {
		tab.mu.Lock()
		tab.setGenerationLocked(s.generation)
		if tab.cancelPump != nil {
			tab.cancelPump()
		}
		if tab.channel != nil {
			channels = append(channels, tab.channel)
			tab.channel = nil
		}
		if tab.grid != nil {
			_ = tab.grid.Reattach(nil)
		}
		tab.mu.Unlock()
	}
	return channels
}

func closeChannels(channels []*channelHandle) {
	for _, channel := range channels {
		_ = channel.Close()
	}
}

func (m *Manager) reap(id string, expectedIdle ...time.Time) error {
	m.mu.Lock()
	session := m.sessions[id]
	if session == nil {
		m.mu.Unlock()
		return nil
	}
	session.mu.Lock()
	retry := session.reapRetry
	if len(expectedIdle) > 0 && !retry && (session.status != StatusConnected || len(session.tabs) != 0 || !session.idleSince.Equal(expectedIdle[0])) {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil
	}
	if session.closed && session.status == StatusDisconnected && !retry {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil
	}
	cancel := session.cancel
	if !retry {
		session.closed = true
		session.status = StatusDisconnected
		session.generation++
		session.finishConnectLocked(ErrSessionClosed)
		session.finishReconnectLocked(ErrSessionClosed)
	}
	transport := session.transport
	tabs := make([]*Tab, 0, len(session.tabs))
	for _, tab := range session.tabs {
		tabs = append(tabs, tab)
	}
	session.mu.Unlock()
	m.mu.Unlock()

	if !retry {
		cancel()
	}
	if transport != nil {
		if err := transport.Close(); err != nil {
			session.mu.Lock()
			session.reapRetry = true
			session.mu.Unlock()
			return err
		}
	}
	remaining := len(tabs)
	completed := false
	var disconnectedEvent StatusEvent
	finish := func(tab *Tab) {
		m.mu.Lock()
		session.mu.Lock()
		if tab != nil {
			if m.tabs[tab.ID] == tab {
				delete(m.tabs, tab.ID)
				delete(m.throttleVersions, tab.ID)
				delete(m.throttleChannels, tab.ID)
			}
			delete(session.tabs, tab.ID)
			remaining--
		}
		if remaining == 0 && !completed && m.sessions[id] == session {
			session.transport = nil
			session.reapRetry = false
			delete(m.sessions, id)
			if m.byAsset[session.assetOwnerKey()] == session.ID {
				delete(m.byAsset, session.assetOwnerKey())
			}
			disconnectedEvent = session.statusEventLocked(StatusDisconnected, nil)
			completed = true
		}
		session.mu.Unlock()
		m.mu.Unlock()
	}
	for _, tab := range tabs {
		if err := m.closeTabResources(tab, func() { finish(tab) }); err != nil {
			session.mu.Lock()
			session.reapRetry = true
			session.mu.Unlock()
			return err
		}
	}
	if len(tabs) == 0 {
		finish(nil)
	}
	if !completed {
		return ErrStaleGeneration
	}
	m.emit(context.Background(), TopicSessionStatus, disconnectedEvent)
	m.transcriptEnded(id)
	return nil
}

func (m *Manager) SweepIdle(ctx context.Context) error {
	if m.idleTimeout < 0 {
		return nil
	}
	type candidate struct {
		id   string
		idle time.Time
	}
	type remoteCandidate struct {
		id       string
		activity time.Time
	}
	now := time.Now()
	m.mu.Lock()
	candidates := make([]candidate, 0)
	remoteCandidates := make([]remoteCandidate, 0)
	for _, session := range m.sessions {
		session.mu.Lock()
		if session.asset.Kind == KindRemote {
			// Remote shells have no "no tabs" idle form: idle means no feed connection, no subscribed watchers,
			// and a timeout since the last feed/management activity; if any condition fails, the shell is not reaped.
			if !session.closed && session.remoteFeeds == 0 && !session.remoteWatchingLocked() && !session.remoteActivity.IsZero() && now.Sub(session.remoteActivity) >= m.idleTimeout {
				remoteCandidates = append(remoteCandidates, remoteCandidate{id: session.ID, activity: session.remoteActivity})
			}
			session.mu.Unlock()
			continue
		}
		if session.status == StatusConnected && len(session.tabs) == 0 && !session.idleSince.IsZero() && now.Sub(session.idleSince) >= m.idleTimeout {
			candidates = append(candidates, candidate{id: session.ID, idle: session.idleSince})
		}
		session.mu.Unlock()
	}
	m.mu.Unlock()
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.disconnect(candidate.id, &candidate.idle); err != nil && !errors.Is(err, ErrSessionNotFound) {
			return err
		}
	}
	for _, candidate := range remoteCandidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.reapRemote(candidate.id, candidate.activity); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) idleLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			_ = m.SweepIdle(m.ctx)
		}
	}
}

func (m *Manager) Close() error {
	m.closeMu.Lock()
	defer m.closeMu.Unlock()
	if m.closeComplete {
		return m.closeErr
	}
	m.mu.Lock()
	m.closed = true
	for channelID, timer := range m.slowKicks {
		timer.Stop()
		delete(m.slowKicks, channelID)
	}
	sessions := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		sessions = append(sessions, id)
	}
	m.mu.Unlock()
	m.cancel()
	errs := make([]error, 0)
	for _, id := range sessions {
		if err := m.reap(id); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		m.closeErr = errors.Join(errs...)
		return m.closeErr
	}
	m.wg.Wait()
	if m.ownsBus {
		_ = m.bus.Close()
	}
	m.closeErr = nil
	m.closeComplete = true
	close(m.closeDone)
	return nil
}

func (m *Manager) startGoroutine(run func()) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		run()
	}()
	return true
}

func (m *Manager) onBackpressure(channelID string, queuedBytes int) {
	m.mu.Lock()
	tabID := m.channelTabs[channelID]
	if tabID == "" {
		m.mu.Unlock()
		return
	}
	set := m.throttleChannels[tabID]
	if set == nil {
		set = make(map[string]bool)
		m.throttleChannels[tabID] = set
	}
	set[channelID] = true
	m.throttleVersions[tabID]++
	version := m.throttleVersions[tabID]
	if m.slowKicks[channelID] == nil {
		m.slowKicks[channelID] = time.AfterFunc(m.slowConsumerGrace, func() { m.kickSlowConsumer(channelID) })
	}
	m.mu.Unlock()
	m.emit(context.Background(), TopicTerminalThrottled, ThrottleEvent{TabID: tabID, ChannelID: channelID, InflightBytes: queuedBytes, Version: version})
}

// kickSlowConsumer kicks a channel that still is not consuming after the backpressure grace expires: the recheck and the removal complete
// under the same m.mu (no recheck-drain race window); after producer.Close the client recovers via the existing reconnect +
// DiscardPending + replay path, and other subscribers' queues are unaffected. Channels that drained or detached during the grace
// are stopped by the recheck (no slowKicks entry or the backpressure mark already cleared).
func (m *Manager) kickSlowConsumer(channelID string) {
	m.mu.Lock()
	if m.slowKicks[channelID] == nil {
		m.mu.Unlock()
		return
	}
	delete(m.slowKicks, channelID)
	tabID := m.channelTabs[channelID]
	if tabID == "" || !m.throttleChannels[tabID][channelID] {
		m.mu.Unlock()
		return
	}
	_, cleanup := m.detachChannelLocked(channelID)
	m.mu.Unlock()
	m.logger.Warn("slow terminal consumer kicked after backpressure grace", "channel", channelID, "tab", tabID, "grace", m.slowConsumerGrace)
	cleanup()
}

func (m *Manager) onDrain(channelID string, queuedBytes int) {
	m.mu.Lock()
	tabID := m.channelTabs[channelID]
	if tabID == "" {
		m.mu.Unlock()
		return
	}
	recovered, version := m.clearThrottleChannelLocked(tabID, channelID)
	m.mu.Unlock()
	if recovered {
		m.emit(context.Background(), TopicTerminalThrottled, ThrottleEvent{TabID: tabID, ChannelID: channelID, InflightBytes: queuedBytes, Recovered: true, Version: version})
	}
}

func (m *Manager) clearThrottleChannelLocked(tabID, channelID string) (bool, uint64) {
	set := m.throttleChannels[tabID]
	if !set[channelID] {
		return false, 0
	}
	delete(set, channelID)
	if timer := m.slowKicks[channelID]; timer != nil {
		timer.Stop()
		delete(m.slowKicks, channelID)
	}
	if len(set) > 0 {
		return false, 0
	}
	m.throttleVersions[tabID]++
	return true, m.throttleVersions[tabID]
}

func (m *Manager) emit(ctx context.Context, topic string, payload any) {
	force := ctx != nil && ctx.Value(recordingControlMarker{}) == true
	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
	}
	if topic == TopicTerminalControl {
		if event, ok := payload.(ControlEvent); ok {
			m.queueRecordingControl(ctx, event, force)
			return
		}
	}
	ownerID, resourceID, closed := m.eventOwner(topic, payload)
	if ownerID != "" {
		ctx = ipc.WithEventOwner(ctx, ownerID)
	}
	if m.emitter == nil {
		return
	}
	_ = m.emitter.EmitSessionEvent(ctx, Event{Topic: topic, Payload: payload})
	if !closed || resourceID == "" {
		return
	}
	m.mu.Lock()
	switch topic {
	case TopicSessionStatus:
		delete(m.sessionOwners, resourceID)
	case TopicTerminalControl:
		delete(m.tabOwners, resourceID)
	}
	m.mu.Unlock()
}

func (m *Manager) eventOwner(topic string, payload any) (ownerID, resourceID string, closed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if topic == TopicSessionStatus {
		event, ok := payload.(StatusEvent)
		if !ok {
			return "", "", false
		}
		if session := m.sessions[event.SessionID]; session != nil {
			session.mu.Lock()
			ownerID = session.userID
			session.mu.Unlock()
			m.sessionOwners[event.SessionID] = ownerID
			return ownerID, event.SessionID, false
		}
		return m.sessionOwners[event.SessionID], event.SessionID, true
	}

	var tabID string
	switch event := payload.(type) {
	case ControlEvent:
		tabID = event.TabID
	case ExitEvent:
		tabID = event.TabID
	case ThrottleEvent:
		tabID = event.TabID
	default:
		return "", "", false
	}
	if tab := m.tabs[tabID]; tab != nil && tab.session != nil {
		tab.session.mu.Lock()
		ownerID = tab.session.userID
		tab.session.mu.Unlock()
		m.tabOwners[tabID] = ownerID
		return ownerID, tabID, false
	}
	return m.tabOwners[tabID], tabID, true
}

func newID() string {
	var data [16]byte
	rand.Read(data[:])
	return hex.EncodeToString(data[:])
}
