package session

import (
	"context"
	"encoding/json"
)

// Live clipboard writes (OSC 52) apply only on the device that most recently
// typed into the tab: with several devices attached, a program's clipboard
// write must not overwrite every device's clipboard. The tab records the
// client whose input it last wrote, and each answering subscriber is told,
// in its stream, whether it is that device. While no input was written, or
// the client that wrote it has no answering subscriber left, every
// subscriber is active, so a single client behaves as before.
//
// Both sides start every anchored stream as active: the client resets at
// each anchor frame, and the server, which records per subscriber what it
// last announced, announces a passive subscriber again after the anchor.
// Announcements are sent under the feed gate after the subscriber's
// coalesced output, so they follow every byte fed before the change; a
// paused subscriber's held bytes still flush after it.

const inputOwnerType = "nexterm.input-owner"

type inputOwnerFrame struct {
	Type   string `json:"type"`
	Active bool   `json:"active"`
}

// noteInputClientLocked records that client's input was written to the
// shell and reports whether the last input client changed. It is the only
// writer of lastInputClient. Callers hold t.mu.
func (t *Tab) noteInputClientLocked(client string) bool {
	client = clientID(client)
	if t.lastInputClient == client {
		return false
	}
	t.lastInputClient = client
	return true
}

// inputPassiveLocked reports whether client's views must ignore live
// clipboard writes. Callers hold t.mu.
func (t *Tab) inputPassiveLocked(client string) bool {
	owner := t.lastInputClient
	if owner == "" || owner == client {
		return false
	}
	for _, subscriber := range t.subscribers {
		if subscriber.answers && subscriber.client == owner {
			return true
		}
	}
	return false
}

// inputOwnerStaleLocked reports whether an answering subscriber's announced
// state differs from its current one. Callers hold t.mu.
func (t *Tab) inputOwnerStaleLocked() bool {
	for _, subscriber := range t.subscribers {
		if subscriber.answers && subscriber.inputPassive != t.inputPassiveLocked(subscriber.client) {
			return true
		}
	}
	return false
}

// syncInputOwner announces changed input-owner states; it takes the feed
// gate.
func (m *Manager) syncInputOwner(tab *Tab) {
	if err := tab.lockFeed(context.Background()); err != nil {
		return
	}
	defer tab.unlockFeed()
	m.syncInputOwnerFeedLocked(tab)
}

// syncInputOwnerFeedLocked records and announces every answering
// subscriber whose state changed. The caller holds the feed gate.
func (m *Manager) syncInputOwnerFeedLocked(tab *Tab) {
	type change struct {
		channelID string
		active    bool
		co        *outCoalescer
		paused    bool
	}
	tab.mu.Lock()
	var changes []change
	for channelID, subscriber := range tab.subscribers {
		if !subscriber.answers {
			continue
		}
		passive := tab.inputPassiveLocked(subscriber.client)
		if passive == subscriber.inputPassive {
			continue
		}
		subscriber.inputPassive = passive
		tab.subscribers[channelID] = subscriber
		changes = append(changes, change{channelID: channelID, active: !passive, co: subscriber.co, paused: subscriber.paused})
	}
	fanout := tab.fanout
	tab.mu.Unlock()
	if fanout == nil {
		return
	}
	for _, change := range changes {
		if !change.paused {
			if batch := change.co.take(); len(batch) > 0 {
				fanout.SendTo(change.channelID, batch)
				m.noteChannelFlushed(tab, change.channelID, int64(len(batch)))
			}
		}
		frame, _ := json.Marshal(inputOwnerFrame{Type: inputOwnerType, Active: change.active})
		fanout.SendJSONTo(change.channelID, frame)
	}
}

// resetInputOwnerLocked records that channelID's client starts a new stream
// as active (an anchor was sent). Callers hold t.mu.
func (t *Tab) resetInputOwnerLocked(channelID string) {
	if subscriber, ok := t.subscribers[channelID]; ok && subscriber.inputPassive {
		subscriber.inputPassive = false
		t.subscribers[channelID] = subscriber
	}
}
