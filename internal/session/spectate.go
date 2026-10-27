package session

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// SpectateSnapshotType is the type value of the read-only preview channel's first frame; the frontend renders the current screen from it.
const SpectateSnapshotType = "snapshot"

// spectateSnapshot is the first frame of a read-only preview channel (JSON text frame): the current screen's plain text lines
// and cursor position, without scrollback or character attributes; afterwards the channel carries only live output (binary frames).
type spectateSnapshot struct {
	Type      string   `json:"type"`
	Lines     []string `json:"lines"`
	CursorRow int      `json:"cursor_row"`
	CursorCol int      `json:"cursor_col"`
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`
}

type SpectateOptions struct {
	ClientID  string
	ChannelID string
}

// AttachSpectator subscribes to a tab's output read-only: inside the feed lock the current screen snapshot is written to the
// channel before the subscription is registered, so there is neither a gap nor duplication between the snapshot and later live output.
// The watcher does not take control, does not participate in grid negotiation, and has no input path to its channel; offline/disconnect
// cleanup goes through the existing DetachChannel. The snapshot carries no character attributes (the server-side screen model stores text only).
func (m *Manager) AttachSpectator(ctx context.Context, tabID string, options SpectateOptions) (TabInfo, error) {
	if options.ChannelID == "" {
		return TabInfo{}, hub.ErrInvalidChannel
	}
	tab, err := m.Tab(tabID)
	if err != nil {
		return TabInfo{}, err
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

	var incarnation uint64
	reserved := false
	producer, err := m.bus.Producer(options.ChannelID, func(token uint64, receiverClosed bool) error {
		incarnation = token
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed || m.tabs[tab.ID] != tab {
			return ErrTabClosed
		}
		if owner := m.channelTabs[options.ChannelID]; owner != "" && owner != tab.ID {
			return errors.New("channel already attached to another tab")
		}
		if receiverClosed {
			tab.mu.Lock()
			reject := tab.ephemeral && !tab.retainable
			tab.mu.Unlock()
			if reject {
				return hub.ErrDetached
			}
		}
		m.channelTabs[options.ChannelID] = tab.ID
		m.channelIncarnations[options.ChannelID] = token
		reserved = true
		return nil
	})
	if err != nil {
		if reserved {
			m.releaseChannelReservation(tab.ID, options.ChannelID, incarnation)
		}
		return TabInfo{}, err
	}
	tab.mu.Lock()
	old, replaced := tab.subscribers[options.ChannelID]
	if replaced {
		tab.dropSubscriberLocked(options.ChannelID)
		tab.syncRecoveryFloorLocked()
		tab.releaseOrphanControllerLocked()
	}
	tab.mu.Unlock()
	if replaced {
		if tab.fanout != nil {
			tab.fanout.Detach(options.ChannelID)
		}
		old.co.teardown()
		_ = old.producer.Close()
	}
	attached := false
	defer func() {
		if !attached {
			_ = producer.Close()
			m.releaseChannelReservation(tab.ID, options.ChannelID, incarnation)
		}
	}()
	if err := m.bus.DiscardPending(options.ChannelID); err != nil {
		return TabInfo{}, err
	}

	frame := spectateSnapshot{Type: SpectateSnapshotType}
	if core, ok := tab.terminal.(interface{ Snapshot() terminal.Snapshot }); ok {
		snapshot := core.Snapshot()
		frame.Lines = snapshot.Lines
		frame.CursorRow = snapshot.CursorRow
		frame.CursorCol = snapshot.CursorCol
		frame.Cols = snapshot.Cols
		frame.Rows = snapshot.Rows
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return TabInfo{}, err
	}
	if err := producer.SendJSON(attachCtx, payload); err != nil {
		return TabInfo{}, err
	}

	m.mu.Lock()
	if m.closed || m.tabs[tab.ID] != tab || m.channelTabs[options.ChannelID] != tab.ID || m.channelIncarnations[options.ChannelID] != incarnation {
		m.mu.Unlock()
		return TabInfo{}, hub.ErrDetached
	}
	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		m.mu.Unlock()
		return TabInfo{}, ErrTabClosed
	}
	co := newOutCoalescer(func() { m.flushSubscriberOutput(tab.ID, options.ChannelID) })
	m.nextAttachmentId++
	tab.subscribers[options.ChannelID] = subscriber{
		client: clientID(options.ClientID), producer: producer, co: co, attachmentId: m.nextAttachmentId,
		episodeFloor: -1, floorHighWater: -1,
	}
	m.attachSubscriberOutput(tab, options.ChannelID, incarnation, producer, co)
	info := tab.infoLocked()
	event := tab.controlEventLocked()
	tab.mu.Unlock()
	m.mu.Unlock()
	attached = true
	m.emit(attachCtx, TopicTerminalControl, event)
	return info, nil
}

// TabOwner returns the account user ID of the session that opened the tab; "" for desktop-local or background sessions.
// tab.session is written at OpenTab creation and never changes, so like runPump it is read without tab.mu,
// avoiding an inversion of the existing session.mu -> tab.mu lock order.
func (m *Manager) TabOwner(tabID string) (string, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return "", err
	}
	session := tab.session
	if session == nil {
		return "", ErrTabNotFound
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.userID, nil
}
