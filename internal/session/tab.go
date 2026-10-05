package session

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/terminal"
	"github.com/ProbiusOfficial/NexTerm/internal/terminal/shellintegr"
	"github.com/ProbiusOfficial/NexTerm/internal/terminalgrid"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

var winRMBanner = []byte("[NexTerm] WinRM line mode; each command runs in a new shell.\r\nPS> ")
var reconnectBanner = []byte("\r\n\x1b[33m[NexTerm] reconnected\x1b[0m\r\n")
var replayClear = []byte("\x1b[2J\x1b[3J\x1b[H")

type subscriber struct {
	client   string
	producer *hub.Producer
}

type Tab struct {
	ID        string
	SessionID string

	session   *Session
	terminal  TerminalState
	durable   base.DurableAttachment
	ephemeral bool

	mu           sync.Mutex
	cols         uint32
	rows         uint32
	gridRevision uint64
	grid         *terminalgrid.Model
	retire       chan struct{}
	channel      *channelHandle
	generation   uint64
	eventVersion uint64
	subscribers  map[string]subscriber
	controller   string
	controlled   bool
	hidden       bool
	exited       bool
	closed       bool
	destroying   bool
	cancelPump   context.CancelFunc
	cwdTracker   *shellintegr.Tracker
	cwd          string
	durableFed   int64

	catchUpRemaining int64

	ctx          context.Context
	cancel       context.CancelFunc
	responses    *responseQueue
	feedGate     chan struct{}
	writeMu      sync.Mutex
	visibilityMu sync.Mutex
	lifecycleMu  sync.Mutex
	closeOnce    sync.Once
}

func (t *Tab) Info() TabInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.infoLocked()
}

func (t *Tab) infoLocked() TabInfo {
	viewers := make(map[string]struct{})
	for _, subscriber := range t.subscribers {
		viewers[subscriber.client] = struct{}{}
	}
	return TabInfo{
		ID: t.ID, SessionID: t.SessionID, Cols: t.cols, Rows: t.rows, GridRevision: t.gridRevision,
		Controller: t.controller, Subscribers: len(t.subscribers), Viewers: len(viewers),
		Exited: t.exited, Ephemeral: t.ephemeral, Durable: t.durable != nil, Cwd: t.cwd,
	}
}

func (t *Tab) controlEventLocked() ControlEvent {
	info := t.infoLocked()
	t.eventVersion++
	t.persistVersionFloorLocked()
	return ControlEvent{
		TabID: t.ID, Cols: info.Cols, Rows: info.Rows, GridRevision: info.GridRevision,
		Controller: info.Controller, Subscribers: info.Subscribers,
		Viewers: info.Viewers, Exited: info.Exited, Version: t.eventVersion, Cwd: t.cwd, Durable: info.Durable,
	}
}

func (t *Tab) exitEventLocked(exitCode *int) ExitEvent {
	t.eventVersion++
	t.persistVersionFloorLocked()
	return ExitEvent{TabID: t.ID, ExitCode: exitCode, Version: t.eventVersion}
}

func (t *Tab) persistVersionFloorLocked() {
	store, ok := t.durable.(durableVersionFloor)
	if !ok {
		return
	}
	_ = store.PersistDurableVersions(t.eventVersion, t.gridRevision)
}

func (m *Manager) OpenTab(ctx context.Context, options OpenTabOptions) (TabInfo, error) {
	if !validSize(options.Cols, options.Rows) {
		return TabInfo{}, ErrInvalidSize
	}
	if options.ChannelID == "" {
		return TabInfo{}, hub.ErrInvalidChannel
	}
	if m.terminals == nil {
		return TabInfo{}, ErrUnsupported
	}
	if options.Durable != nil {
		if options.Ephemeral || options.Durable.Recover && options.TabID == "" {
			return TabInfo{}, ErrInvalidOptions
		}
	}
	tabID := options.TabID
	if tabID == "" && options.Durable != nil {
		tabID = ids.New()
	} else if tabID == "" {
		tabID = m.newID()
	}

	m.mu.Lock()
	session := m.sessions[options.SessionID]
	if session == nil {
		m.mu.Unlock()
		return TabInfo{}, ErrSessionNotFound
	}
	if options.TabID != "" && m.tabs[tabID] != nil {
		m.mu.Unlock()
		return TabInfo{}, ErrTabExists
	}
	session.mu.Lock()
	if m.closed || session.closed {
		session.mu.Unlock()
		m.mu.Unlock()
		return TabInfo{}, ErrSessionClosed
	}
	if session.status != StatusConnected || session.transport == nil {
		session.mu.Unlock()
		m.mu.Unlock()
		return TabInfo{}, ErrDisconnected
	}
	generation := session.generation
	transport := session.transport
	kind := session.asset.Kind
	encoding := session.asset.Encoding
	session.mu.Unlock()
	m.mu.Unlock()
	var provider base.DurableProvider
	if options.Durable != nil {
		var err error
		provider, err = m.durableProviderFor(ctx, session, transport, kind, generation)
		if err != nil {
			return TabInfo{}, err
		}
	}
	if err := validateEncoding(encoding); err != nil {
		return TabInfo{}, err
	}

	cols, rows := options.Cols, options.Rows
	var channel *channelHandle
	var durableAttachment base.DurableAttachment
	destroyOnFailure := options.Durable != nil && !options.Durable.Recover
	var terminal TerminalState
	cleanup := func() error {
		var killErr error
		if destroyOnFailure && durableAttachment != nil {
			killErr = durableAttachment.Kill(context.Background())
		}
		if channel != nil {
			_ = channel.Close()
		}
		if terminal != nil {
			terminal.Close()
		}
		return killErr
	}
	var err error
	if options.Durable != nil {
		var opened base.DurableAttachment
		if options.Durable.Recover {
			opened, err = provider.Attach(ctx, tabID)
		} else {
			opened, err = provider.Create(ctx, base.DurableCreateOptions{
				ID: tabID, Command: options.Durable.Command, Dir: options.Durable.Dir, Env: options.Durable.Env,
				Cols: options.Cols, Rows: options.Rows,
			})
		}
		if opened != nil {
			durableAttachment = opened
			channel = newChannelHandle(opened)
		}
		if err != nil {
			return TabInfo{}, errors.Join(err, cleanup())
		}
		if channel == nil {
			return TabInfo{}, errors.Join(errors.New("durable provider returned a nil attachment"), cleanup())
		}
		if options.Durable.Recover {
			if grid, ok := durableAttachment.(durableGridSource); ok {
				if actualCols, actualRows, ok := grid.DurableGrid(); ok && validSize(actualCols, actualRows) {
					cols, rows = actualCols, actualRows
				}
			}
		}
	} else if ptyBacked(kind) {
		ptyTransport, ok := transport.Transport.(base.PTYTransport)
		if !ok {
			return TabInfo{}, errors.Join(ErrUnsupported, cleanup())
		}
		term := options.Term
		if term == "" {
			term = "xterm-256color"
		}
		opened, err := ptyTransport.OpenPTY(ctx, base.PTYOptions{
			Cols: options.Cols, Rows: options.Rows, Term: term, ExpectedGeneration: transport.Generation(),
		})
		if opened != nil {
			channel = newChannelHandle(opened)
		}
		if err != nil {
			return TabInfo{}, errors.Join(err, cleanup())
		}
		if channel == nil {
			return TabInfo{}, errors.Join(errors.New("transport returned a nil PTY"), cleanup())
		}
	}

	terminal, err = m.terminals.NewTerminal(TerminalConfig{
		TabID: tabID, SessionID: session.ID, Cols: cols, Rows: rows, Encoding: encoding,
	})
	if err != nil {
		return TabInfo{}, errors.Join(err, cleanup())
	}

	tabCtx, cancel := context.WithCancel(m.ctx)
	tab := &Tab{
		ID: tabID, SessionID: session.ID, session: session, terminal: terminal,
		ephemeral: options.Ephemeral, cols: cols, rows: rows,
		channel: channel, durable: durableAttachment, generation: generation, subscribers: make(map[string]subscriber),
		ctx: tabCtx, cancel: cancel, responses: newResponseQueue(generation), feedGate: make(chan struct{}, 1),
		cwdTracker: shellintegr.NewTracker(),
	}
	if options.Durable != nil {
		if source, ok := m.durable.(durableTranscriptOffsetSource); ok {
			if options.Durable.Recover {
				tab.catchUpRemaining = source.DurableTranscriptCatchUpBytes(tabID)
			} else {
				source.PersistDurableTranscriptOffset(tabID, 0)
			}
		}
	}
	if options.Durable != nil && options.Durable.Recover {
		if store, ok := durableAttachment.(durableVersionFloor); ok {
			if eventVersion, gridRevision, err := store.DurableVersions(); err == nil {
				tab.eventVersion = eventVersion
				tab.gridRevision = gridRevision
			}
		}
	}
	tab.feedGate <- struct{}{}
	if err := m.initGrid(tab, channel, generation); err != nil {
		cancel()
		return TabInfo{}, errors.Join(err, cleanup())
	}
	terminal.SetResponseHandler(tab.responses.enqueue)

	m.mu.Lock()
	session.mu.Lock()
	current := !m.closed && !session.closed && m.sessions[session.ID] == session && session.status == StatusConnected && session.generation == generation && session.transport == transport && m.tabs[tab.ID] == nil
	if current {
		m.tabs[tab.ID] = tab
		session.tabs[tab.ID] = tab
		session.idleSince = time.Time{}
	}
	session.mu.Unlock()
	m.mu.Unlock()
	if !current {
		cancel()
		cleanupErr := cleanup()
		if options.TabID != "" {
			m.mu.Lock()
			exists := m.tabs[tab.ID] != nil
			m.mu.Unlock()
			if exists {
				return TabInfo{}, errors.Join(ErrTabExists, cleanupErr)
			}
		}
		return TabInfo{}, errors.Join(ErrStaleGeneration, cleanupErr)
	}
	if !m.startResponses(tab) {
		return TabInfo{}, errors.Join(ErrSessionClosed, m.closeTab(tab, destroyOnFailure))
	}

	info, err := m.attach(ctx, tab, AttachOptions{ClientID: options.ClientID, ChannelID: options.ChannelID}, false)
	if err != nil {
		return TabInfo{}, errors.Join(err, m.closeTab(tab, destroyOnFailure))
	}
	if kind == KindWinRM {
		if err := m.feed(ctx, tab, generation, winRMBanner); err != nil {
			return TabInfo{}, errors.Join(err, m.closeTab(tab, destroyOnFailure))
		}
	} else if !m.startPump(tab, channel, generation) {
		return TabInfo{}, errors.Join(ErrSessionClosed, m.closeTab(tab, destroyOnFailure))
	}
	return info, nil
}

func (m *Manager) AttachTab(ctx context.Context, tabID string, options AttachOptions) (TabInfo, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return TabInfo{}, err
	}
	return m.attach(ctx, tab, options, true)
}

func (m *Manager) attach(ctx context.Context, tab *Tab, options AttachOptions, clear bool) (TabInfo, error) {
	if options.ChannelID == "" {
		return TabInfo{}, hub.ErrInvalidChannel
	}
	if options.ReplayBytes < 0 {
		options.ReplayBytes = m.defaultReplay
	}
	attachCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(tab.ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	if err := tab.lockFeed(attachCtx); err != nil {
		return TabInfo{}, err
	}
	defer tab.unlockFeed()

	m.mu.Lock()
	if m.closed || m.tabs[tab.ID] != tab {
		m.mu.Unlock()
		return TabInfo{}, ErrTabClosed
	}
	if owner := m.channelTabs[options.ChannelID]; owner != "" && owner != tab.ID {
		m.mu.Unlock()
		return TabInfo{}, errors.New("channel already attached to another tab")
	}
	m.channelTabs[options.ChannelID] = tab.ID
	m.mu.Unlock()

	tab.mu.Lock()
	old, replaced := tab.subscribers[options.ChannelID]
	if replaced {
		delete(tab.subscribers, options.ChannelID)
		tab.releaseOrphanControllerLocked()
	}
	tab.mu.Unlock()
	producer, err := m.bus.Producer(options.ChannelID)
	if err != nil {
		m.releaseChannelReservation(tab.ID, options.ChannelID)
		return TabInfo{}, err
	}
	if replaced {
		_ = old.producer.Close()
	}
	attached := false
	defer func() {
		if !attached {
			_ = producer.Close()
			m.releaseChannelReservation(tab.ID, options.ChannelID)
		}
	}()
	if err := m.bus.DiscardPending(options.ChannelID); err != nil {
		return TabInfo{}, err
	}
	if clear {
		if err := producer.SendBinary(attachCtx, replayClear); err != nil {
			return TabInfo{}, err
		}
	}
	var replay []byte
	if options.ReplayBytes > 0 {
		replay = tab.terminal.Dump(options.ReplayBytes)
	}
	for len(replay) > 0 {
		size := min(len(replay), 64<<10)
		if err := producer.SendBinary(attachCtx, replay[:size]); err != nil {
			return TabInfo{}, err
		}
		replay = replay[size:]
	}

	m.mu.Lock()
	if m.closed || m.tabs[tab.ID] != tab || m.channelTabs[options.ChannelID] != tab.ID {
		m.mu.Unlock()
		return TabInfo{}, hub.ErrDetached
	}
	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		m.mu.Unlock()
		return TabInfo{}, ErrTabClosed
	}
	client := clientID(options.ClientID)
	tab.subscribers[options.ChannelID] = subscriber{client: client, producer: producer}
	if tab.controller == "" {
		if tab.controlled && tab.grid != nil {
			if err := tab.grid.Claim(); err != nil {
				tab.mu.Unlock()
				m.mu.Unlock()
				return TabInfo{}, mapGridError(err)
			}
		}
		tab.controller = client
		tab.controlled = true
	}
	info := tab.infoLocked()
	event := tab.controlEventLocked()
	tab.mu.Unlock()
	m.mu.Unlock()
	attached = true
	m.emit(attachCtx, TopicTerminalControl, event)
	return info, nil
}

func (m *Manager) releaseChannelReservation(tabID, channelID string) {
	m.mu.Lock()
	if m.channelTabs[channelID] == tabID {
		delete(m.channelTabs, channelID)
	}
	m.mu.Unlock()
}

func (t *Tab) releaseOrphanControllerLocked() {
	if t.controller == "" {
		return
	}
	for _, subscriber := range t.subscribers {
		if subscriber.client == t.controller {
			return
		}
	}
	t.controller = ""
	if t.grid != nil && !t.hidden {
		_ = t.grid.SetMode(terminalgrid.ModeObserver)
	}
}

func (t *Tab) lockFeed(ctx context.Context) error {
	select {
	case <-t.feedGate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-t.ctx.Done():
		return ErrTabClosed
	}
}

func (t *Tab) unlockFeed() {
	t.feedGate <- struct{}{}
}

func (m *Manager) DetachChannel(channelID string) error {
	m.mu.Lock()
	tab := m.tabs[m.channelTabs[channelID]]
	if tab == nil {
		delete(m.channelTabs, channelID)
		m.mu.Unlock()
		_ = m.bus.CloseChannel(channelID)
		return nil
	}
	tab.mu.Lock()
	subscriber, attached := tab.subscribers[channelID]
	delete(m.channelTabs, channelID)
	delete(tab.subscribers, channelID)
	throttleRecovered, throttleVersion := m.clearThrottleChannelLocked(tab.ID, channelID)
	tab.releaseOrphanControllerLocked()
	event := tab.controlEventLocked()
	empty := len(tab.subscribers) == 0
	ephemeral := tab.ephemeral
	tab.mu.Unlock()
	m.mu.Unlock()
	if attached {
		_ = subscriber.producer.Close()
	} else {
		_ = m.bus.CloseChannel(channelID)
	}
	m.emit(context.Background(), TopicTerminalControl, event)
	if throttleRecovered {
		m.emit(context.Background(), TopicTerminalThrottled, ThrottleEvent{TabID: tab.ID, ChannelID: channelID, Recovered: true, Version: throttleVersion})
	}
	if attached && empty && ephemeral {
		return m.CloseTab(tab.ID)
	}
	return nil
}

func (m *Manager) DetachClient(tabID, client string) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	return m.detachMatching(tab, func(subscriberClient string) bool {
		return subscriberClient == clientID(client)
	})
}

func (m *Manager) DetachAll(tabID string) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	return m.detachMatching(tab, func(string) bool { return true })
}

func (m *Manager) detachMatching(tab *Tab, match func(string) bool) error {
	m.mu.Lock()
	if m.tabs[tab.ID] != tab {
		m.mu.Unlock()
		return ErrTabClosed
	}
	tab.mu.Lock()
	detached := make([]subscriber, 0)
	throttleRecovered := false
	var throttleVersion uint64
	var throttleChannel string
	for channel, subscriber := range tab.subscribers {
		if match(subscriber.client) {
			detached = append(detached, subscriber)
			delete(tab.subscribers, channel)
			delete(m.channelTabs, channel)
			if recovered, version := m.clearThrottleChannelLocked(tab.ID, channel); recovered {
				throttleRecovered = true
				throttleVersion = version
				throttleChannel = channel
			}
		}
	}
	tab.releaseOrphanControllerLocked()
	event := tab.controlEventLocked()
	empty := len(tab.subscribers) == 0
	ephemeral := tab.ephemeral
	tab.mu.Unlock()
	m.mu.Unlock()
	for _, subscriber := range detached {
		_ = subscriber.producer.Close()
	}
	if len(detached) > 0 {
		m.emit(context.Background(), TopicTerminalControl, event)
	}
	if throttleRecovered {
		m.emit(context.Background(), TopicTerminalThrottled, ThrottleEvent{TabID: tab.ID, ChannelID: throttleChannel, Recovered: true, Version: throttleVersion})
	}
	if len(detached) > 0 && empty && ephemeral {
		return m.CloseTab(tab.ID)
	}
	return nil
}

func (m *Manager) Claim(tabID, client string) (string, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return "", err
	}
	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		return "", ErrTabClosed
	}
	previous := tab.controller
	if tab.grid != nil {
		if err := tab.grid.Claim(); err != nil {
			tab.mu.Unlock()
			return "", mapGridError(err)
		}
	}
	tab.controller = clientID(client)
	tab.controlled = true
	event := tab.controlEventLocked()
	tab.mu.Unlock()
	m.emit(context.Background(), TopicTerminalControl, event)
	return previous, nil
}

func (m *Manager) Release(tabID, client string) (bool, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return false, err
	}
	tab.mu.Lock()
	released := tab.controller == clientID(client)
	if released && tab.grid != nil && !tab.hidden {
		if err := tab.grid.SetMode(terminalgrid.ModeObserver); err != nil {
			tab.mu.Unlock()
			return false, mapGridError(err)
		}
	}
	if released {
		tab.controller = ""
	}
	event := tab.controlEventLocked()
	tab.mu.Unlock()
	if released {
		m.emit(context.Background(), TopicTerminalControl, event)
	}
	return released, nil
}

func (m *Manager) Write(ctx context.Context, tabID, client string, data []byte) error {
	m.notifyUserInput(tabID)
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	tab.writeMu.Lock()
	defer tab.writeMu.Unlock()
	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		return ErrTabClosed
	}
	if tab.controller != clientID(client) {
		tab.mu.Unlock()
		return ErrNotController
	}
	if !ptyBacked(tab.session.asset.Kind) {
		tab.mu.Unlock()
		return ErrUnsupported
	}
	channel := tab.channel
	generation := tab.generation
	exited := tab.exited
	tab.mu.Unlock()
	tab.session.mu.Lock()
	connected := tab.session.status == StatusConnected && tab.session.generation == generation
	tab.session.mu.Unlock()
	if !connected {
		return ErrDisconnected
	}
	if exited || channel == nil {
		return ErrTabClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeAll(channel, data)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func validateEncoding(encoding string) error {
	if encoding == "" {
		return nil
	}
	_, err := terminal.ParseEncoding(encoding)
	return err
}

func (m *Manager) Resize(ctx context.Context, tabID, client string, cols, rows uint32) error {
	if !validSize(cols, rows) {
		return ErrInvalidSize
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		return ErrTabClosed
	}
	if tab.controller != clientID(client) {
		tab.mu.Unlock()
		return ErrNotController
	}
	channel := tab.channel
	generation := tab.generation
	exited := tab.exited
	grid := tab.grid
	tab.mu.Unlock()
	tab.session.mu.Lock()
	connected := tab.session.status == StatusConnected && tab.session.generation == generation
	tab.session.mu.Unlock()
	if !connected {
		return ErrDisconnected
	}
	if ptyBacked(tab.session.asset.Kind) && (exited || channel == nil) {
		return ErrTabClosed
	}
	if grid == nil {
		return ErrUnsupported
	}

	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		return ErrTabClosed
	}
	if tab.controller != clientID(client) {
		tab.mu.Unlock()
		return ErrNotController
	}
	if tab.generation != generation || tab.channel != channel {
		tab.mu.Unlock()
		return ErrDisconnected
	}
	revision, err := grid.SetDesired(terminalgrid.Grid{Cols: int(cols), Rows: int(rows)})
	tab.mu.Unlock()
	if err != nil {
		return mapGridError(err)
	}
	return mapGridError(grid.Wait(ctx, revision))
}

func (m *Manager) ExecLine(ctx context.Context, tabID, client, command string) (base.ExecResult, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return base.ExecResult{}, err
	}
	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		return base.ExecResult{}, ErrTabClosed
	}
	if tab.session.asset.Kind != KindWinRM {
		tab.mu.Unlock()
		return base.ExecResult{}, ErrUnsupported
	}
	if tab.controller != clientID(client) {
		tab.mu.Unlock()
		return base.ExecResult{}, ErrNotController
	}
	generation := tab.generation
	tab.mu.Unlock()
	tab.session.mu.Lock()
	if tab.session.status != StatusConnected || tab.session.transport == nil || tab.session.generation != generation {
		tab.session.mu.Unlock()
		return base.ExecResult{}, ErrDisconnected
	}
	transport := tab.session.transport
	tab.session.mu.Unlock()
	result, execErr := transport.Exec(ctx, command, base.ExecOptions{ExpectedGeneration: transport.Generation()})
	if result.Stdout != "" {
		if err := m.feed(ctx, tab, generation, []byte(result.Stdout)); err != nil {
			return result, err
		}
	}
	if result.Stderr != "" {
		if err := m.feed(ctx, tab, generation, []byte(result.Stderr)); err != nil {
			return result, err
		}
	}
	if err := m.feed(ctx, tab, generation, []byte("\r\nPS> ")); err != nil {
		return result, err
	}
	return result, execErr
}

func (m *Manager) CloseTab(id string) error {
	m.mu.Lock()
	tab := m.tabs[id]
	m.mu.Unlock()
	return m.closeTab(tab, true)
}

func (m *Manager) closeTab(tab *Tab, destroyDurable bool) error {
	if tab == nil {
		return nil
	}
	id := tab.ID
	tab.lifecycleMu.Lock()
	defer tab.lifecycleMu.Unlock()

	m.mu.Lock()
	if m.tabs[id] != tab {
		m.mu.Unlock()
		return nil
	}
	if destroyDurable && tab.durable != nil {
		tab.mu.Lock()
		tab.destroying = true
		tab.mu.Unlock()
		m.mu.Unlock()
		if err := tab.durable.Kill(context.Background()); err != nil {
			tab.mu.Lock()
			tab.destroying = false
			tab.mu.Unlock()
			return err
		}
	} else {
		m.mu.Unlock()
	}

	m.mu.Lock()
	if m.tabs[id] == tab {
		session := tab.session
		session.mu.Lock()
		delete(m.tabs, id)
		delete(session.tabs, id)
		delete(m.throttleVersions, id)
		delete(m.throttleChannels, id)
		if len(session.tabs) == 0 {
			session.idleSince = time.Now()
		}
		session.mu.Unlock()
	}
	m.mu.Unlock()
	m.closeTabResources(tab)
	return nil
}

func (m *Manager) closeTabResources(tab *Tab) {
	tab.closeOnce.Do(func() {
		tab.mu.Lock()
		tab.closed = true
		tab.exited = true
		tab.setGenerationLocked(tab.generation + 1)
		if tab.cancelPump != nil {
			tab.cancelPump()
		}
		channel := tab.channel
		tab.channel = nil
		subscribers := tab.subscribers
		tab.subscribers = make(map[string]subscriber)
		tab.controller = ""
		if tab.grid != nil {
			tab.grid.Close()
			close(tab.retire)
		}
		tab.mu.Unlock()
		tab.responses.close()
		tab.cancel()

		m.mu.Lock()
		for channelID := range subscribers {
			delete(m.channelTabs, channelID)
		}
		m.mu.Unlock()
		for _, subscriber := range subscribers {
			_ = subscriber.producer.Close()
		}
		if channel != nil {
			_ = channel.Close()
		}
		<-tab.feedGate
		tab.terminal.Close()
		tab.feedGate <- struct{}{}
		tab.mu.Lock()
		event := tab.controlEventLocked()
		tab.mu.Unlock()
		m.emit(context.Background(), TopicTerminalControl, event)
	})
}

func (m *Manager) feed(ctx context.Context, tab *Tab, generation uint64, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	feedCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(tab.ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	if err := tab.lockFeed(feedCtx); err != nil {
		return err
	}
	defer tab.unlockFeed()
	tab.mu.Lock()
	if tab.closed || tab.generation != generation {
		tab.mu.Unlock()
		return ErrStaleGeneration
	}
	tab.mu.Unlock()
	tab.terminal.Feed(data)
	m.transcriptOutput(feedCtx, tab, data)
	if _, changed := tab.observeCWD(data); changed {
		tab.mu.Lock()
		event := tab.controlEventLocked()
		tab.mu.Unlock()
		m.emit(feedCtx, TopicTerminalControl, event)
	}
	tab.mu.Lock()
	subscribers := make([]subscriber, 0, len(tab.subscribers))
	for _, subscriber := range tab.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	tab.mu.Unlock()
	for _, subscriber := range subscribers {
		err := subscriber.producer.SendBinary(feedCtx, data)
		if errors.Is(err, hub.ErrClosed) || errors.Is(err, hub.ErrReplaced) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) startPump(tab *Tab, channel *channelHandle, generation uint64) bool {
	tab.mu.Lock()
	if tab.closed || tab.generation != generation || tab.channel != channel {
		tab.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(tab.session.ctx)
	tab.cancelPump = cancel
	tab.mu.Unlock()
	started := m.startGoroutine(func() {
		defer cancel()
		m.runPump(ctx, tab, channel, generation)
	})
	if !started {
		tab.mu.Lock()
		tab.cancelPump = nil
		tab.mu.Unlock()
		cancel()
	}
	return started
}

func (m *Manager) runPump(ctx context.Context, tab *Tab, channel *channelHandle, generation uint64) {
	defer channel.Close()
	buffer := make([]byte, 64<<10)
	var readErr error
	for {
		count, err := channel.Read(buffer)
		if count > 0 {
			chunk := append([]byte(nil), buffer[:count]...)
			feedErr := m.feed(ctx, tab, generation, chunk)
			if feedErr != nil && ctx.Err() != nil {
				readErr = feedErr
				break
			}
			if feedErr == nil {
				tab.addDurableFed(int64(count))
			}
		}
		if err != nil {
			readErr = err
			break
		}
		if count == 0 {
			select {
			case <-ctx.Done():
				readErr = ctx.Err()
			default:
			}
			if readErr != nil {
				break
			}
		}
	}
	var waitErr error
	if ctx.Err() == nil {
		waitErr = channel.Wait(ctx)
	}
	exitCode, hasExitCode := base.ExitCode(waitErr)
	if !hasExitCode {
		exitCode, hasExitCode = base.ExitCode(readErr)
	}

	var code *int
	if hasExitCode {
		code = &exitCode
	}
	tab.mu.Lock()
	current := !tab.closed && !tab.destroying && tab.generation == generation
	var exitEvent ExitEvent
	var control ControlEvent
	if current {
		tab.exited = true
		if tab.channel == channel {
			tab.channel = nil
		}
		if tab.grid != nil {
			_ = tab.grid.Reattach(nil)
		}
		exitEvent = tab.exitEventLocked(code)
		control = tab.controlEventLocked()
	}
	tab.mu.Unlock()
	if !current {
		return
	}
	m.emit(context.Background(), TopicTerminalExit, exitEvent)
	m.emit(context.Background(), TopicTerminalControl, control)

	tab.session.mu.Lock()
	transport := tab.session.transport
	shouldReconnect := tab.session.status == StatusConnected && tab.session.generation == generation && reconnectable(tab.session.asset.Kind) && transport != nil && !transport.IsAlive()
	tab.session.mu.Unlock()
	if shouldReconnect {
		m.StartReconnect(tab.session.ID)
	}
}
