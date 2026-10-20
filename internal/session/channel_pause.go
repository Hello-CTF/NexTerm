package session

import (
	"context"
	"encoding/json"
)

// Channel output pause (client-driven producer backpressure). A saturated
// terminal view pauses its channel instead of detaching it: the subscriber's
// coalescer keeps buffering output (bounded by outputCoalesceMax plus one pump
// read), the pump stalls once a paused subscriber is full so the PTY read
// blocks, and no byte is dropped or reordered. Resume flushes the held batch
// in stream order. Unlike DetachChannel, pausing never reaps the tab, so
// ephemeral local/WinRM tabs and their PTYs stay alive across the episode.

func (m *Manager) tabIDForChannel(channelID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.channelTabs[channelID]
}

// PauseChannelFor stops feeding output to one channel without touching the
// attachment: the tab, its PTY, and other subscribers stay live. The client's
// accepted offset opens a retention floor: the recovery ring keeps everything
// at or after it until the episode resolves, stalling the producer instead of
// evicting the refused suffix. A negative acceptedBytes leaves the floor
// unchanged.
func (m *Manager) PauseChannelFor(ctx context.Context, channelID string, acceptedBytes int64) error {
	tabID := m.tabIDForChannel(channelID)
	if tabID == "" {
		return ErrTabNotFound
	}
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return err
	}
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	tab.mu.Lock()
	subscriber, ok := tab.subscribers[channelID]
	if ok {
		if !subscriber.paused {
			subscriber.paused = true
			tab.subscribers[channelID] = subscriber
		}
		if acceptedBytes >= 0 {
			floor := min(max(acceptedBytes, 0), subscriber.flushedOffset)
			tab.setEpisodeFloorLocked(channelID, floor)
		}
	}
	tab.signalOutputWakeLocked()
	tab.mu.Unlock()
	return nil
}

// ResumeChannelFor releases a paused channel and flushes whatever the
// coalescer held, restoring stream order behind already-delivered bytes. The
// client only resumes once it caught up, so its retention floor closes.
func (m *Manager) ResumeChannelFor(ctx context.Context, channelID string) error {
	tabID := m.tabIDForChannel(channelID)
	if tabID == "" {
		return ErrTabNotFound
	}
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return err
	}
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	tab.mu.Lock()
	subscriber, ok := tab.subscribers[channelID]
	if ok && subscriber.paused {
		subscriber.paused = false
		tab.subscribers[channelID] = subscriber
	}
	if ok {
		tab.setEpisodeFloorLocked(channelID, -1)
	}
	tab.signalOutputWakeLocked()
	tab.mu.Unlock()
	m.flushSubscriberOutput(tabID, channelID)
	return nil
}

// outputStalled reports whether a paused subscriber's coalescer is full: the
// pump must stop reading the PTY until the wake channel fires, applying
// backpressure to the shell instead of growing memory without bound. A
// recovery floor at capacity stalls it the same way: the ring must not evict
// a subscriber's unresolved refused suffix.
func (t *Tab) outputStalled() (bool, chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, subscriber := range t.subscribers {
		if subscriber.paused && subscriber.co.len() >= outputCoalesceMax {
			return true, t.outputWake
		}
	}
	if t.recovery != nil && t.recovery.needsStall() {
		return true, t.outputWake
	}
	return false, nil
}

// setEpisodeFloorLocked records a subscriber's accepted offset for an open
// refusal episode and re-applies the tab's retention floor; a negative
// accepted offset closes the episode.
func (t *Tab) setEpisodeFloorLocked(channelID string, accepted int64) {
	subscriber, ok := t.subscribers[channelID]
	if !ok {
		return
	}
	subscriber.episodeFloor = accepted
	t.subscribers[channelID] = subscriber
	t.syncRecoveryFloorLocked()
}

// syncRecoveryFloorLocked re-applies the retention floor: the minimum
// accepted offset among subscribers with an open episode, or -1 when none.
func (t *Tab) syncRecoveryFloorLocked() {
	if t.recovery == nil {
		return
	}
	floor := int64(-1)
	for _, subscriber := range t.subscribers {
		if subscriber.episodeFloor >= 0 && (floor < 0 || subscriber.episodeFloor < floor) {
			floor = subscriber.episodeFloor
		}
	}
	t.recovery.setFloor(floor)
}

func (t *Tab) signalOutputWakeLocked() {
	if t.outputWake == nil {
		t.outputWake = make(chan struct{})
		return
	}
	close(t.outputWake)
	t.outputWake = make(chan struct{})
}

// noteChannelFlushed records the absolute stream offset a flush carried a
// channel to, so a saturated client can later ask for everything produced
// after its accepted offset.
func (m *Manager) noteChannelFlushed(tab *Tab, channelID string, bytes int64) {
	tab.mu.Lock()
	subscriber, ok := tab.subscribers[channelID]
	if ok {
		subscriber.flushedOffset += bytes
		tab.subscribers[channelID] = subscriber
	}
	tab.mu.Unlock()
}

// RecoverChannelFor redelivers the suffix a saturated client refused: the
// client reports its accepted offset in absolute stream coordinates (the
// attach or recovery anchor's streamSeq plus the bytes accepted since it),
// and the tab's recovery ring replays everything the server flushed after
// that point, in order, without reaping the tab. A synthetic anchor frame
// opens the replay so the client can tell it apart from stale in-flight
// bytes. Bytes still held in the paused coalescer are not replayed: the
// resume flush delivers them behind the replay in stream order, so nothing
// is sent twice and nothing is skipped.
//
// The anchor and the replay travel the same ordered fanout queue as live
// output, so frames already queued for the subscriber deliver first and the
// refused suffix arrives exactly once. The reported offset also advances the
// ring's retention floor: the suffix cannot be evicted before the client
// catches up, and a caught-up report closes the episode. The whole
// snapshot-compute-send sequence runs under the feed gate: the flushed
// offset, the ring content, and the anchor describe one consistent stream
// snapshot with no feed interleaving. When the accepted offset has already
// been evicted from the bounded ring the suffix cannot be redelivered whole;
// ErrRecoveryOverrun reports that gap instead of returning a short replay
// that would silently drop the prefix.
//
// It returns the server's flushed offset, so the client knows when it has
// caught up.
func (m *Manager) RecoverChannelFor(ctx context.Context, channelID string, acceptedBytes int64) (int64, error) {
	tabID := m.tabIDForChannel(channelID)
	if tabID == "" {
		return 0, ErrTabNotFound
	}
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return 0, err
	}
	tab, err := m.Tab(tabID)
	if err != nil {
		return 0, err
	}
	if err := tab.lockFeed(ctx); err != nil {
		return 0, err
	}
	defer tab.unlockFeed()
	tab.mu.Lock()
	subscriber, ok := tab.subscribers[channelID]
	recovery := tab.recovery
	fanout := tab.fanout
	tab.mu.Unlock()
	if !ok || recovery == nil || fanout == nil {
		return 0, ErrUnsupported
	}
	refused := subscriber.flushedOffset - acceptedBytes
	if refused <= 0 {
		tab.mu.Lock()
		tab.setEpisodeFloorLocked(channelID, -1)
		tab.mu.Unlock()
		return subscriber.flushedOffset, nil
	}
	if oldest := recovery.oldestOffset(); oldest < 0 || acceptedBytes < oldest {
		return 0, ErrRecoveryOverrun
	}
	replay := recovery.replayRange(acceptedBytes, subscriber.flushedOffset)
	if int64(len(replay)) != refused {
		return 0, ErrRecoveryOverrun
	}
	// Advance the retention floor to the reported accepted offset: the ring
	// keeps everything at or after it while the episode is unresolved.
	tab.mu.Lock()
	tab.setEpisodeFloorLocked(channelID, acceptedBytes)
	tab.mu.Unlock()
	// Pause the channel for the replay: live output buffers in the coalescer
	// and the resume flush delivers it in order behind the replay, so nothing
	// interleaves with the refused suffix.
	tab.mu.Lock()
	if !subscriber.paused {
		subscriber.paused = true
		tab.subscribers[channelID] = subscriber
		tab.signalOutputWakeLocked()
	}
	tab.mu.Unlock()
	anchor, err := json.Marshal(streamAnchor{
		Type:      streamAnchorType,
		Epoch:     "recovery-" + tabID,
		StreamSeq: uint64(acceptedBytes),
	})
	if err != nil {
		return 0, err
	}
	if !fanout.SendJSONToWait(ctx, channelID, anchor) {
		return 0, ErrTabClosed
	}
	for len(replay) > 0 {
		size := min(len(replay), 64<<10)
		if !fanout.SendToWait(ctx, channelID, replay[:size]) {
			return 0, ErrTabClosed
		}
		replay = replay[size:]
	}
	return subscriber.flushedOffset, nil
}
