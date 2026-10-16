package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/terminal/shellintegr"
	"github.com/Hello-CTF/NexTerm/internal/terminalgrid"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var winRMBanner = []byte("[NexTerm] WinRM line mode; each command runs in a new shell.\r\nPS> ")
var reconnectBanner = []byte("\r\n\x1b[33m[NexTerm] reconnected\x1b[0m\r\n")
var replayClear = []byte("\x1b[2J\x1b[3J\x1b[H")

type subscriber struct {
	client   string
	producer *hub.Producer
	co       *outCoalescer
}

const streamAnchorType = "nexterm.stream-anchor"

// streamAnchor is the first frame of an attach output stream (hub JSON frame), telling the client this segment's sequence
// anchor: streamSeq is the ring absolute sequence number of the first non-control byte, controlBytes is the number of
// control bytes at the stream head that do not count toward the sequence (clear-screen escape); expired means the client
// anchor was not adopted and this attach fell back to a full clear-screen replay. The client computes the consumed
// sequence as streamSeq + max(0, bytes received - controlBytes) and brings it back via AttachOptions.FromSeq/FromEpoch on reconnect.
type streamAnchor struct {
	Type         string `json:"type"`
	Epoch        string `json:"epoch"`
	StreamSeq    uint64 `json:"streamSeq"`
	ControlBytes int    `json:"controlBytes,omitempty"`
	Expired      bool   `json:"expired,omitempty"`
}

type Tab struct {
	ID        string
	SessionID string

	session    *Session
	terminal   TerminalState
	durable    base.DurableAttachment
	ephemeral  bool
	retainable bool
	shell      Shell

	mu           sync.Mutex
	cols         uint32
	rows         uint32
	gridRevision uint64
	grid         *terminalgrid.Model
	retire       chan struct{}
	channel      *channelHandle
	generation   uint64
	eventVersion uint64
	encoding     string
	subscribers  map[string]subscriber
	retained     bool
	retainedBy   string
	controller   string
	controlled   bool
	// guestLease/guestLeaseUntil record the preview guest control lease holder and expiry:
	// after the controller is removed by orphan cleanup on disconnect, the lease is kept until expiry (disconnect grace) and lazily cleared.
	guestLease       string
	guestLeaseUntil  time.Time
	hidden           bool
	exited           bool
	closed           bool
	destroying       bool
	title            string
	cancelPump       context.CancelFunc
	cwdTracker       *shellintegr.Tracker
	cwd              string
	commandTrack     *shellintegr.CommandTracker
	commandState     shellintegr.CommandState
	pendingCmd       *pendingCommand
	durableFed       int64
	durableIdentity  base.DurableIdentity
	metadataRevision uint64
	renameAttempts   map[string]*durableRenameAttempt
	streamEpoch      string

	catchUpRemaining int64

	ctx          context.Context
	cancel       context.CancelFunc
	responses    *responseQueue
	feedGate     chan struct{}
	writeMu      sync.Mutex
	visibilityMu sync.Mutex
	lifecycleMu  sync.Mutex
	closeMu      sync.Mutex
	closeRetry   bool
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
		Exited: t.exited, Retainable: t.retainable || !t.ephemeral, Ephemeral: t.ephemeral, Durable: t.durable != nil, Cwd: t.cwd, Encoding: t.encoding,
		Title: t.title, Shell: t.shell,
	}
}

func (t *Tab) controlEventLocked() ControlEvent {
	queue := recordingControlOrderForTab(t)
	queue.mu.Lock()
	queue.orderHeld.Store(true)
	info := t.infoLocked()
	t.eventVersion++
	t.persistVersionFloorLocked()
	event := ControlEvent{
		TabID: t.ID, Cols: info.Cols, Rows: info.Rows, GridRevision: info.GridRevision,
		Controller: info.Controller, Subscribers: info.Subscribers,
		Viewers: info.Viewers, Exited: info.Exited, Version: t.eventVersion, Cwd: t.cwd, Durable: info.Durable,
		Title: t.title,
	}
	queue.pending[event.Version] = recordingControlEvent{event: event, closed: t.closed}
	queue.orderHeld.Store(false)
	queue.mu.Unlock()
	return event
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
	if options.ChannelID == "" && !options.Detached {
		return TabInfo{}, hub.ErrInvalidChannel
	}
	if options.ChannelID != "" && options.Detached {
		return TabInfo{}, ErrInvalidOptions
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
		tabID = newID()
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
	if !sessionOwnerAllowed(session, userIDFromContext(ctx)) {
		session.mu.Unlock()
		m.mu.Unlock()
		return TabInfo{}, ErrForbidden
	}
	if session.status != StatusConnected || session.transport == nil {
		session.mu.Unlock()
		m.mu.Unlock()
		return TabInfo{}, ErrDisconnected
	}
	// Remote shells are a pushed feed with no backend channel; opening a tab here would attach to nothing.
	if session.Asset().Kind == KindRemote {
		session.mu.Unlock()
		m.mu.Unlock()
		return TabInfo{}, ErrUnsupported
	}
	generation := session.generation
	transport := session.transport
	kind := session.asset.Kind
	encoding := session.asset.Encoding
	startupCommand := session.asset.StartupCommand
	session.mu.Unlock()
	m.mu.Unlock()
	if options.Retainable && (kind != KindLocal || !options.Ephemeral || options.Durable != nil) {
		return TabInfo{}, ErrInvalidOptions
	}
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
			m.deleteDurableTranscriptOffset(tabID)
			m.deleteDurableTabTitle(tabID)
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
			opened, err = attachRecoveredDurable(ctx, provider, tabID, options.Durable.Identity)
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

	var durableIdentity base.DurableIdentity
	var metadataRevision uint64
	if options.Durable != nil {
		metadataRevision = options.Durable.MetadataRevision
		if options.Durable.Identity != nil {
			durableIdentity = *options.Durable.Identity
		} else {
			durableIdentity, _ = durableAttachmentIdentity(durableAttachment)
		}
	}
	terminal, err = m.terminals.NewTerminal(TerminalConfig{
		TabID: tabID, SessionID: session.ID, Cols: cols, Rows: rows, Encoding: encoding,
	})
	if err != nil {
		return TabInfo{}, errors.Join(err, cleanup())
	}

	tabCtx, cancel := context.WithCancel(m.ctx)
	shell := ShellUnknown
	if kind == KindLocal && options.Durable == nil {
		if local, ok := transport.Transport.(interface{ Shell() string }); ok {
			classified := Shell(local.Shell())
			switch classified {
			case ShellPowerShell, ShellCmd, ShellPOSIX:
				shell = classified
			}
		}
	}
	tab := &Tab{
		ID: tabID, SessionID: session.ID, session: session, terminal: terminal,
		ephemeral: options.Ephemeral, retainable: options.Retainable, cols: cols, rows: rows, encoding: canonicalEncoding(encoding),
		channel: channel, durable: durableAttachment, generation: generation, subscribers: make(map[string]subscriber),
		shell: shell,
		ctx:   tabCtx, cancel: cancel, responses: newResponseQueue(generation), feedGate: make(chan struct{}, 1),
		cwdTracker: shellintegr.NewTracker(), commandTrack: shellintegr.NewCommandTracker(),
		durableIdentity: durableIdentity, metadataRevision: metadataRevision,
		streamEpoch: ids.New(),
	}
	if options.Durable != nil {
		if m.offsets != nil {
			if options.Durable.Recover {
				tab.catchUpRemaining = m.offsets.DurableTranscriptCatchUpBytes(tabID)
				if tab.catchUpRemaining > 0 {
					m.logger.Info("durable transcript catch-up armed; skipping already-recorded replay bytes", "tab", tabID, "bytes", tab.catchUpRemaining)
				}
			} else {
				m.offsets.PersistDurableTranscriptOffset(tabID, 0)
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
		if options.Durable.CanonicalMetadata {
			tab.title = options.Durable.Title
		} else if title := m.durableTabTitle(tabID); title != "" {
			tab.title = title
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

	var info TabInfo
	if options.Detached {
		tab.mu.Lock()
		info = tab.infoLocked()
		event := tab.controlEventLocked()
		tab.mu.Unlock()
		m.emit(ctx, TopicTerminalControl, event)
	} else {
		info, err = m.attach(ctx, tab, AttachOptions{ClientID: options.ClientID, ChannelID: options.ChannelID}, false)
		if err != nil {
			return TabInfo{}, errors.Join(err, m.closeTab(tab, destroyOnFailure))
		}
	}
	m.transcriptResize(ctx, tab, cols, rows)
	if kind == KindWinRM {
		if err := m.feed(ctx, tab, generation, winRMBanner); err != nil {
			return TabInfo{}, errors.Join(err, m.closeTab(tab, destroyOnFailure))
		}
	} else if !m.startPump(tab, channel, generation) {
		return TabInfo{}, errors.Join(ErrSessionClosed, m.closeTab(tab, destroyOnFailure))
	}
	if options.Durable == nil && startupCommand != "" && startupCommandSupported(kind) {
		_ = writeAll(channel, []byte(startupCommand+"\n"))
	}
	return info, nil
}

// CreateTabOptions describes a detached terminal tab opened on behalf of a
// client that is not (yet) attached, such as an AI-created terminal.
type CreateTabOptions struct {
	SessionID string
	Cols      uint32
	Rows      uint32
	Title     string
	// Command is an optional startup command written to the new shell; only
	// kinds with createTabCommandSupported accept it, others reject the call.
	Command string
}

// CreateTab opens a detached terminal tab on the session with the same
// durable/PTY routing the attach flow uses: local sessions get an ephemeral
// tab, device sessions a daemon-durable tab, everything else a plain PTY tab.
// The tab starts with no subscribers; clients attach via AttachTab.
func (m *Manager) CreateTab(ctx context.Context, options CreateTabOptions) (TabInfo, error) {
	session, err := m.Session(options.SessionID)
	if err != nil {
		return TabInfo{}, err
	}
	kind := session.Asset().Kind
	// Remote shells have no backend channel; creating a tab there would panic in OpenTab. Other kinds
	// (local/ssh/docker/device/winrm) all support detached creation.
	if kind == KindRemote {
		return TabInfo{}, ErrUnsupported
	}
	if options.Command != "" && !createTabCommandSupported(kind) {
		return TabInfo{}, ErrUnsupported
	}
	var title string
	if options.Title != "" {
		title, err = normalizeTabTitle(options.Title)
		if err != nil {
			return TabInfo{}, err
		}
	}
	cols, rows := options.Cols, options.Rows
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	open := OpenTabOptions{
		SessionID: options.SessionID, Cols: cols, Rows: rows,
		Ephemeral: kind == KindLocal, Detached: true,
	}
	if kind == KindDevice {
		open.Durable = &DurableTabOptions{}
	}
	info, err := m.OpenTab(ctx, open)
	if err != nil {
		return TabInfo{}, err
	}
	if title != "" {
		info, err = m.RenameTab(ctx, info.ID, title)
		if err != nil {
			return TabInfo{}, errors.Join(err, m.CloseTab(info.ID))
		}
	}
	if options.Command != "" {
		tab, err := m.Tab(info.ID)
		if err != nil {
			return TabInfo{}, err
		}
		tab.mu.Lock()
		channel := tab.channel
		exited := tab.exited
		tab.mu.Unlock()
		if exited || channel == nil {
			return TabInfo{}, errors.Join(ErrTabClosed, m.CloseTab(info.ID))
		}
		if err := writeAll(channel, []byte(options.Command+"\n")); err != nil {
			return TabInfo{}, errors.Join(err, m.CloseTab(info.ID))
		}
	}
	return info, nil
}

// RenameTab sets the custom title of a terminal tab and emits a control event
// so every attached client refreshes. Durable tabs persist the title and
// restore it on recovery; other tabs keep it in memory for their lifetime.
func (m *Manager) RenameTab(ctx context.Context, tabID, title string) (TabInfo, error) {
	title, err := normalizeTabTitle(title)
	if err != nil {
		return TabInfo{}, err
	}
	tab, err := m.Tab(tabID)
	if err != nil {
		return TabInfo{}, err
	}
	tab.lifecycleMu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			tab.lifecycleMu.Unlock()
		}
	}()
	tab.session.mu.Lock()
	allowed := sessionOwnerAllowed(tab.session, userIDFromContext(ctx))
	transport := tab.session.transport
	generation := tab.session.generation
	kind := tab.session.asset.Kind
	status := tab.session.status
	tab.session.mu.Unlock()
	if !allowed {
		return TabInfo{}, ErrForbidden
	}
	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		return TabInfo{}, ErrTabClosed
	}
	durable := tab.durable != nil
	tab.mu.Unlock()
	if durable && status != StatusConnected {
		return TabInfo{}, ErrDisconnected
	}
	if durable {
		provider, err := m.durableProviderFor(ctx, tab.session, transport, kind, generation)
		if err != nil {
			return TabInfo{}, err
		}
		supported, err := durableMetadataSupported(ctx, provider)
		if err != nil {
			return TabInfo{}, err
		}
		if supported {
			unlocked = true
			tab.lifecycleMu.Unlock()
			return m.renameCanonicalTab(ctx, tab, provider, title)
		}
	}
	tab.mu.Lock()
	tab.title = title
	event := tab.controlEventLocked()
	info := tab.infoLocked()
	tab.mu.Unlock()
	if durable {
		m.persistDurableTabTitle(tabID, title)
	}
	m.emit(ctx, TopicTerminalControl, event)
	return info, nil
}

func (m *Manager) AttachTab(ctx context.Context, tabID string, options AttachOptions) (TabInfo, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return TabInfo{}, err
	}
	tab.session.mu.Lock()
	allowed := sessionOwnerAllowed(tab.session, userIDFromContext(ctx))
	tab.session.mu.Unlock()
	if !allowed {
		return TabInfo{}, ErrForbidden
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
		old.co.teardown(old.producer)
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
	anchor, control, replay := m.planAttachReplay(tab, options, clear)
	if anchor != nil {
		frame, _ := json.Marshal(anchor)
		if err := producer.SendJSON(attachCtx, frame); err != nil {
			return TabInfo{}, err
		}
	}
	if len(control) > 0 {
		if err := producer.SendBinary(attachCtx, control); err != nil {
			return TabInfo{}, err
		}
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
	tab.subscribers[options.ChannelID] = subscriber{
		client:   client,
		producer: producer,
		co:       newOutCoalescer(func() { m.flushSubscriberOutput(tab.ID, options.ChannelID) }),
	}
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

// planAttachReplay computes the output stream to send for one attach: when the client anchor (FromSeq +
// FromEpoch) is adoptable, it resumes from that offset without clear-screen control bytes; when the anchor is
// missing, the epoch mismatches, it has rolled out of the ring, or the gap exceeds the ReplayBytes limit, it falls
// back to the existing clear-screen + full replay and marks the anchor expired. Terminals without sequence tracking have a nil anchor and no anchor frame is sent.
func (m *Manager) planAttachReplay(tab *Tab, options AttachOptions, clear bool) (*streamAnchor, []byte, []byte) {
	core, ok := tab.terminal.(interface {
		ReplayFrom(uint64, int) ([]byte, uint64, bool)
		LatestSeq() uint64
	})
	if !ok {
		var replay []byte
		if options.ReplayBytes > 0 {
			replay = tab.terminal.Dump(options.ReplayBytes)
		}
		if clear {
			return nil, replayClear, replay
		}
		return nil, nil, replay
	}
	anchor := &streamAnchor{Type: streamAnchorType, Epoch: tab.streamEpoch}
	if options.FromSeq > 0 {
		anchor.Expired = true
		if options.FromEpoch == anchor.Epoch {
			data, start, clamped := core.ReplayFrom(options.FromSeq, 0)
			gap := core.LatestSeq() - start
			if !clamped && (options.ReplayBytes == 0 || gap <= uint64(options.ReplayBytes)) {
				anchor.Expired = false
				anchor.StreamSeq = start
				return anchor, nil, data
			}
		}
	}
	var replay []byte
	if options.ReplayBytes > 0 {
		replay = tab.terminal.Dump(options.ReplayBytes)
	}
	anchor.StreamSeq = core.LatestSeq() - uint64(len(replay))
	if clear {
		anchor.ControlBytes = len(replayClear)
		return anchor, replayClear, replay
	}
	return anchor, nil, replay
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

// ScreenSeq returns the tab's monotonic output sequence: the total bytes ever
// pushed into the scrollback ring. Pair it with Manager.OutputSince to read
// only output produced after an anchor. It returns 0 for terminals without a
// sequence-tracked ring.
func (t *Tab) ScreenSeq() uint64 {
	core, ok := t.terminal.(interface{ LatestSeq() uint64 })
	if !ok {
		return 0
	}
	return core.LatestSeq()
}

func (m *Manager) DetachChannel(channelID string) error {
	m.mu.Lock()
	_, cleanup := m.detachChannelLocked(channelID)
	m.mu.Unlock()
	cleanup()
	return nil
}

// detachChannelLocked removes the channel registration under m.mu and returns the cleanup action to run after unlocking
// (producer close, event emission, ephemeral tab reaping); shared by DetachChannel and kickSlowConsumer,
// keeping the recheck and the removal atomic.
func (m *Manager) detachChannelLocked(channelID string) (string, func()) {
	tab := m.tabs[m.channelTabs[channelID]]
	if tab == nil {
		delete(m.channelTabs, channelID)
		return "", func() { _ = m.bus.CloseChannel(channelID) }
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
	generation := tab.generation
	tabID := tab.ID
	tab.mu.Unlock()
	return tabID, func() {
		if attached {
			subscriber.co.teardown(subscriber.producer)
			_ = subscriber.producer.Close()
		} else {
			_ = m.bus.CloseChannel(channelID)
		}
		m.emit(context.Background(), TopicTerminalControl, event)
		if throttleRecovered {
			m.emit(context.Background(), TopicTerminalThrottled, ThrottleEvent{TabID: tabID, ChannelID: channelID, Recovered: true, Version: throttleVersion})
		}
		if attached && empty && ephemeral {
			_ = m.reapEphemeralTab(tab, generation)
		}
	}
}

func (m *Manager) DetachClient(tabID, client string) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	return m.detachMatching(tab, func(subscriberClient string) bool {
		return subscriberClient == clientID(client)
	}, false, "")
}

func (m *Manager) DetachAll(tabID string) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	return m.detachMatching(tab, func(string) bool { return true }, false, "")
}

func (m *Manager) BackgroundTab(ctx context.Context, tabID, client string) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	tab.session.mu.Lock()
	allowed := sessionOwnerAllowed(tab.session, userIDFromContext(ctx))
	tab.session.mu.Unlock()
	if !allowed {
		return ErrForbidden
	}
	client = clientID(client)
	return m.detachMatching(tab, func(subscriberClient string) bool {
		return subscriberClient == client
	}, true, client)
}

func (m *Manager) detachMatching(tab *Tab, match func(string) bool, background bool, backgroundClient string) error {
	m.mu.Lock()
	if m.tabs[tab.ID] != tab || background && m.closed {
		m.mu.Unlock()
		return ErrTabClosed
	}
	tab.mu.Lock()
	if background && tab.ephemeral {
		if !tab.retainable {
			tab.mu.Unlock()
			m.mu.Unlock()
			return ErrUnsupported
		}
		if tab.closed || tab.destroying || tab.exited || tab.channel == nil {
			tab.mu.Unlock()
			m.mu.Unlock()
			return ErrTabClosed
		}
	}
	if background && tab.retainable {
		matched := 0
		for _, subscriber := range tab.subscribers {
			if match(subscriber.client) {
				matched++
			}
		}
		if matched == 0 {
			if tab.retained && tab.retainedBy == backgroundClient {
				tab.mu.Unlock()
				m.mu.Unlock()
				return nil
			}
			tab.mu.Unlock()
			m.mu.Unlock()
			return ErrDisconnected
		}
		tab.retained = true
		tab.retainedBy = backgroundClient
	}
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
	generation := tab.generation
	tab.mu.Unlock()
	m.mu.Unlock()
	for _, subscriber := range detached {
		subscriber.co.teardown(subscriber.producer)
		_ = subscriber.producer.Close()
	}
	if len(detached) > 0 {
		m.emit(context.Background(), TopicTerminalControl, event)
	}
	if throttleRecovered {
		m.emit(context.Background(), TopicTerminalThrottled, ThrottleEvent{TabID: tab.ID, ChannelID: throttleChannel, Recovered: true, Version: throttleVersion})
	}
	if !background && len(detached) > 0 && empty && ephemeral {
		return m.reapEphemeralTab(tab, generation)
	}
	return nil
}

var ephemeralReapAfterCurrent = func() {}

func (m *Manager) reapEphemeralTab(tab *Tab, generation uint64) error {
	tab.lifecycleMu.Lock()
	defer tab.lifecycleMu.Unlock()

	m.mu.Lock()
	session := tab.session
	session.mu.Lock()
	tab.mu.Lock()
	current := m.tabs[tab.ID] == tab && session.tabs[tab.ID] == tab && !session.closed &&
		tab.generation == generation && session.generation == generation && tab.ephemeral && !tab.retained &&
		!tab.closed && (!tab.destroying || tab.closeRetry) && len(tab.subscribers) == 0
	if current {
		tab.destroying = true
	}
	tab.mu.Unlock()
	session.mu.Unlock()
	m.mu.Unlock()
	ephemeralReapAfterCurrent()
	if !current {
		return nil
	}
	if err := m.closeTabResources(tab); err != nil {
		return err
	}

	m.mu.Lock()
	session.mu.Lock()
	tab.mu.Lock()
	if m.tabs[tab.ID] == tab && session.tabs[tab.ID] == tab {
		delete(m.tabs, tab.ID)
		delete(session.tabs, tab.ID)
		delete(m.throttleVersions, tab.ID)
		delete(m.throttleChannels, tab.ID)
		if len(session.tabs) == 0 {
			session.idleSince = time.Now()
		}
	}
	tab.mu.Unlock()
	session.mu.Unlock()
	m.mu.Unlock()
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
	tab.clearGuestLeaseLocked()
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
	if err := writeAll(channel, data); err != nil {
		return err
	}
	m.transcriptInput(ctx, tab, data)
	return nil
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
	if err := mapGridError(grid.Wait(ctx, revision)); err != nil {
		return err
	}
	m.transcriptResize(ctx, tab, cols, rows)
	return nil
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
	m.recordExecCommand(ctx, tab, command, result)
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
		m.deleteDurableTranscriptOffset(id)
		m.deleteDurableTabTitle(id)
	} else {
		m.mu.Unlock()
		if err := m.closeTabResources(tab); err != nil {
			return err
		}
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
	_ = m.closeTabResources(tab)
	return nil
}

func (m *Manager) closeTabResources(tab *Tab, afterClose ...func()) error {
	tab.closeMu.Lock()
	defer tab.closeMu.Unlock()
	tab.mu.Lock()
	if tab.closed {
		tab.mu.Unlock()
		if len(afterClose) > 0 && afterClose[0] != nil {
			afterClose[0]()
		}
		return nil
	}
	tab.destroying = true
	channel := tab.channel
	tab.mu.Unlock()
	if channel != nil {
		if err := channel.Close(); err != nil {
			tab.mu.Lock()
			tab.closeRetry = true
			tab.mu.Unlock()
			return err
		}
	}

	tab.mu.Lock()
	tab.closeRetry = false
	tab.closed = true
	tab.exited = true
	tab.setGenerationLocked(tab.generation + 1)
	if tab.cancelPump != nil {
		tab.cancelPump()
	}
	tab.channel = nil
	subscribers := tab.subscribers
	tab.subscribers = make(map[string]subscriber)
	tab.controller = ""
	if tab.grid != nil {
		tab.grid.Close()
		close(tab.retire)
	}
	tab.mu.Unlock()
	if len(afterClose) > 0 && afterClose[0] != nil {
		afterClose[0]()
	}
	tab.responses.close()
	tab.cancel()

	m.mu.Lock()
	for channelID := range subscribers {
		delete(m.channelTabs, channelID)
	}
	m.mu.Unlock()
	for _, subscriber := range subscribers {
		subscriber.co.teardown(subscriber.producer)
		_ = subscriber.producer.Close()
	}
	<-tab.feedGate
	m.flushPendingCommand(tab)
	tab.terminal.Close()
	tab.feedGate <- struct{}{}
	tab.mu.Lock()
	event := tab.controlEventLocked()
	tab.mu.Unlock()
	m.emit(context.Background(), TopicTerminalControl, event)
	return nil
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
	m.observeCommand(tab, data)
	tab.mu.Lock()
	subscribers := make([]subscriber, 0, len(tab.subscribers))
	for _, subscriber := range tab.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	tab.mu.Unlock()
	for _, subscriber := range subscribers {
		batch, sendNow := subscriber.co.add(data)
		if !sendNow {
			continue
		}
		err := subscriber.producer.SendBinary(feedCtx, batch)
		subscriber.co.sent()
		if errors.Is(err, hub.ErrClosed) || errors.Is(err, hub.ErrReplaced) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// flushSubscriberOutput is the flush when a coalescing window fires: like feed, it takes the buffered batch and sends under feedGate,
// preserving relative order with live output and replay frames.
func (m *Manager) flushSubscriberOutput(tabID, channelID string) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return
	}
	if err := tab.lockFeed(context.Background()); err != nil {
		return
	}
	defer tab.unlockFeed()
	tab.mu.Lock()
	subscriber, ok := tab.subscribers[channelID]
	tab.mu.Unlock()
	if !ok {
		return
	}
	batch := subscriber.co.take()
	if len(batch) == 0 {
		return
	}
	_ = subscriber.producer.SendBinary(tab.ctx, batch)
	subscriber.co.sent()
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
		_ = m.StartReconnect(tab.session.ID)
	}
}

func (m *Manager) OpenTabFor(ctx context.Context, options OpenTabOptions) (TabInfo, error) {
	if _, err := m.SessionFor(ctx, options.SessionID); err != nil {
		return TabInfo{}, err
	}
	return m.OpenTab(ctx, options)
}

func (m *Manager) CreateTabFor(ctx context.Context, options CreateTabOptions) (TabInfo, error) {
	if _, err := m.SessionFor(ctx, options.SessionID); err != nil {
		return TabInfo{}, err
	}
	return m.CreateTab(ctx, options)
}

func (m *Manager) AttachTabFor(ctx context.Context, tabID string, options AttachOptions) (TabInfo, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return TabInfo{}, err
	}
	return m.AttachTab(ctx, tabID, options)
}

func (m *Manager) RenameTabFor(ctx context.Context, tabID, title string) (TabInfo, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return TabInfo{}, err
	}
	return m.RenameTab(ctx, tabID, title)
}

func (m *Manager) DetachChannelFor(ctx context.Context, channelID string) error {
	m.mu.Lock()
	tabID := m.channelTabs[channelID]
	m.mu.Unlock()
	if tabID == "" {
		if userIDFromContext(ctx) != "" {
			return ErrTabNotFound
		}
		if !m.requestOpenAccess(ctx) {
			return ErrForbidden
		}
		return m.DetachChannel(channelID)
	}
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return err
	}
	return m.DetachChannel(channelID)
}

func (m *Manager) DetachClientFor(ctx context.Context, tabID, client string) error {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return err
	}
	return m.DetachClient(tabID, client)
}

func (m *Manager) DetachAllFor(ctx context.Context, tabID string) error {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return err
	}
	return m.DetachAll(tabID)
}

func (m *Manager) ClaimFor(ctx context.Context, tabID, client string) (string, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return "", err
	}
	return m.Claim(tabID, client)
}

func (m *Manager) ReleaseFor(ctx context.Context, tabID, client string) (bool, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return false, err
	}
	return m.Release(tabID, client)
}

func (m *Manager) WriteFor(ctx context.Context, tabID, client string, data []byte) error {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return err
	}
	return m.Write(ctx, tabID, client, data)
}

func (m *Manager) ResizeFor(ctx context.Context, tabID, client string, cols, rows uint32) error {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return err
	}
	return m.Resize(ctx, tabID, client, cols, rows)
}

func (m *Manager) ExecLineFor(ctx context.Context, tabID, client, command string) (base.ExecResult, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return base.ExecResult{}, err
	}
	return m.ExecLine(ctx, tabID, client, command)
}

func (m *Manager) CloseTabFor(ctx context.Context, id string) error {
	if _, err := m.TabFor(ctx, id); err != nil {
		return err
	}
	return m.CloseTab(id)
}

func (m *Manager) GridSnapshotFor(ctx context.Context, tabID string) (terminalgrid.Snapshot, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return terminalgrid.Snapshot{}, err
	}
	return m.GridSnapshot(tabID)
}

func (m *Manager) FlushResizeFor(ctx context.Context, tabID, client string) error {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return err
	}
	return m.FlushResize(ctx, tabID, client)
}
