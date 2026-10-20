package session

import "context"

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
// attachment: the tab, its PTY, and other subscribers stay live.
func (m *Manager) PauseChannelFor(ctx context.Context, channelID string) error {
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
	if ok && !subscriber.paused {
		subscriber.paused = true
		tab.subscribers[channelID] = subscriber
	}
	tab.signalOutputWakeLocked()
	tab.mu.Unlock()
	return nil
}

// ResumeChannelFor releases a paused channel and flushes whatever the
// coalescer held, restoring stream order behind already-delivered bytes.
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
	tab.signalOutputWakeLocked()
	tab.mu.Unlock()
	m.flushSubscriberOutput(tabID, channelID)
	return nil
}

// outputStalled reports whether a paused subscriber's coalescer is full: the
// pump must stop reading the PTY until the wake channel fires, applying
// backpressure to the shell instead of growing memory without bound.
func (t *Tab) outputStalled() (bool, chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, subscriber := range t.subscribers {
		if subscriber.paused && subscriber.co.len() >= outputCoalesceMax {
			return true, t.outputWake
		}
	}
	return false, nil
}

func (t *Tab) signalOutputWakeLocked() {
	if t.outputWake == nil {
		t.outputWake = make(chan struct{})
		return
	}
	close(t.outputWake)
	t.outputWake = make(chan struct{})
}
