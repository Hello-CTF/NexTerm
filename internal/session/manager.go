package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/hub"
)

type Manager struct {
	mu          sync.Mutex
	sessions    map[string]*Session
	byAsset     map[string]string
	tabs        map[string]*Tab
	channelTabs map[string]string
	closed      bool
	started     bool

	connector Connector
	terminals TerminalFactory
	bus       *hub.Hub
	ownsBus   bool
	emitter   Emitter
	newID     func() string

	hookMu        sync.RWMutex
	userInputHook func(string)

	idleTimeout      time.Duration
	sweepInterval    time.Duration
	reconnectMax     int
	reconnectBackoff []time.Duration
	defaultReplay    int

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeDone chan struct{}
}

func NewManager(config Config) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		sessions:         make(map[string]*Session),
		byAsset:          make(map[string]string),
		tabs:             make(map[string]*Tab),
		channelTabs:      make(map[string]string),
		connector:        config.Connector,
		terminals:        config.Terminals,
		emitter:          config.Emitter,
		newID:            config.NewID,
		idleTimeout:      config.IdleTimeout,
		sweepInterval:    config.SweepInterval,
		reconnectMax:     config.ReconnectMax,
		reconnectBackoff: append([]time.Duration(nil), config.ReconnectBackoff...),
		defaultReplay:    config.DefaultReplay,
		ctx:              ctx,
		cancel:           cancel,
		closeDone:        make(chan struct{}),
	}
	if manager.newID == nil {
		manager.newID = newID
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
	if config.Hub == nil {
		manager.bus = hub.New(hub.Options{
			OnChannelClose: func(channelID string) { _ = manager.DetachChannel(channelID) },
			OnBackpressure: manager.onBackpressure,
		})
		manager.ownsBus = true
	} else {
		manager.bus = config.Hub
	}
	return manager
}

func (m *Manager) Hub() *hub.Hub {
	return m.bus
}

func (m *Manager) Bind(channelID string) (*hub.Receiver, error) {
	return m.bus.Bind(channelID)
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

func (m *Manager) Connect(ctx context.Context, asset Asset) (*Session, error) {
	if asset.ID == "" {
		return nil, errors.New("asset id is empty")
	}
	if asset.Kind != KindLocal && asset.Kind != KindSSH && asset.Kind != KindDocker && asset.Kind != KindWinRM {
		return nil, fmt.Errorf("%w: asset kind %s", ErrUnsupported, asset.Kind)
	}
	if m.connector == nil {
		return nil, fmt.Errorf("%w: connector", ErrUnsupported)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrSessionClosed
	}
	if existing := m.sessions[m.byAsset[asset.ID]]; existing != nil {
		existing.mu.Lock()
		status := existing.status
		done := existing.connectDone
		existing.mu.Unlock()
		m.mu.Unlock()
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
		ID:          m.newID(),
		CreatedAt:   time.Now(),
		asset:       asset,
		status:      StatusConnecting,
		generation:  generation,
		ctx:         sessionCtx,
		cancel:      cancel,
		tabs:        make(map[string]*Tab),
		idleSince:   time.Now(),
		connectDone: make(chan struct{}),
	}
	m.sessions[session.ID] = session
	m.byAsset[asset.ID] = session.ID
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
		delete(m.byAsset, asset.ID)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[id]
	if session == nil {
		return nil, ErrSessionNotFound
	}
	return session, nil
}

func (m *Manager) Tab(id string) (*Tab, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tab := m.tabs[id]
	if tab == nil {
		return nil, ErrTabNotFound
	}
	return tab, nil
}

func (m *Manager) ListSessions() []SessionInfo {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()
	infos := make([]SessionInfo, 0, len(sessions))
	for _, session := range sessions {
		infos = append(infos, session.Info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos
}

func (m *Manager) ListTabs() []TabInfo {
	m.mu.Lock()
	tabs := make([]*Tab, 0, len(m.tabs))
	for _, tab := range m.tabs {
		tabs = append(tabs, tab)
	}
	m.mu.Unlock()
	infos := make([]TabInfo, 0, len(tabs))
	for _, tab := range tabs {
		infos = append(infos, tab.Info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos
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
	if m.byAsset[session.asset.ID] == session.ID {
		delete(m.byAsset, session.asset.ID)
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
	if len(expectedIdle) > 0 && (session.status != StatusConnected || len(session.tabs) != 0 || !session.idleSince.Equal(expectedIdle[0])) {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil
	}
	if session.closed && session.status == StatusDisconnected {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil
	}
	session.closed = true
	session.status = StatusDisconnected
	session.generation++
	cancel := session.cancel
	transport := session.transport
	session.transport = nil
	session.finishConnectLocked(ErrSessionClosed)
	session.finishReconnectLocked(ErrSessionClosed)
	tabs := make([]*Tab, 0, len(session.tabs))
	for _, tab := range session.tabs {
		tabs = append(tabs, tab)
		delete(m.tabs, tab.ID)
	}
	session.tabs = make(map[string]*Tab)
	delete(m.sessions, id)
	if m.byAsset[session.asset.ID] == session.ID {
		delete(m.byAsset, session.asset.ID)
	}
	disconnectedEvent := session.statusEventLocked(StatusDisconnected, nil)
	session.mu.Unlock()
	m.mu.Unlock()

	cancel()
	for _, tab := range tabs {
		m.closeTabResources(tab)
	}
	if transport != nil {
		_ = transport.Close()
	}
	m.emit(context.Background(), TopicSessionStatus, disconnectedEvent)
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
	now := time.Now()
	m.mu.Lock()
	candidates := make([]candidate, 0)
	for _, session := range m.sessions {
		session.mu.Lock()
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
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		sessions := make([]string, 0, len(m.sessions))
		for id := range m.sessions {
			sessions = append(sessions, id)
		}
		m.mu.Unlock()
		m.cancel()
		for _, id := range sessions {
			_ = m.reap(id)
		}
		m.wg.Wait()
		if m.ownsBus {
			_ = m.bus.Close()
		}
		close(m.closeDone)
	})
	<-m.closeDone
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
	m.mu.Unlock()
	if tabID != "" {
		m.emit(context.Background(), TopicTerminalThrottled, ThrottleEvent{TabID: tabID, InflightBytes: queuedBytes})
	}
}

func (m *Manager) emit(ctx context.Context, topic string, payload any) {
	if m.emitter == nil {
		return
	}
	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
	}
	_ = m.emitter.EmitSessionEvent(ctx, Event{Topic: topic, Payload: payload})
}

var fallbackID atomic.Uint64

func newID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err == nil {
		return hex.EncodeToString(data[:])
	}
	return fmt.Sprintf("session-%d", fallbackID.Add(1))
}
