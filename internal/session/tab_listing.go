package session

import (
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// dropSubscriberLocked removes a subscriber; when it was the last
// interactive one the tab went to the background now. Spectators (read-only
// previews) come and go without moving the time. The caller holds t.mu.
func (t *Tab) dropSubscriberLocked(channelID string) {
	removed, ok := t.subscribers[channelID]
	if !ok {
		return
	}
	delete(t.subscribers, channelID)
	if removed.spectator {
		return
	}
	t.detaches++
	if !t.hasInteractiveSubscriberLocked() {
		t.detachedAt = time.Now()
	}
}

// hasInteractiveSubscriberLocked reports a subscriber other than a spectator.
// The caller holds t.mu.
func (t *Tab) hasInteractiveSubscriberLocked() bool {
	for _, subscriber := range t.subscribers {
		if !subscriber.spectator {
			return true
		}
	}
	return false
}

// detachedAtLocked is when the last interactive subscriber left, zero while
// one is attached. The caller holds t.mu.
func (t *Tab) detachedAtLocked() int64 {
	if t.hasInteractiveSubscriberLocked() {
		return 0
	}
	return unixMilliOrZero(t.detachedAt)
}

// detachedSinceOpenLocked reports that DetachedAt is only when this app run
// reopened an existing helper session: no client has left it since, so the
// real detach happened no later than that. The caller holds t.mu.
func (t *Tab) detachedSinceOpenLocked() bool {
	return t.reopened && t.detaches == 0 && !t.hasInteractiveSubscriberLocked()
}

func unixMilliOrZero(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UnixMilli()
}

// durableAttentionState reads the helper's attention state from a durable
// attachment (a remote stream or an in-process attachment) without copying
// its events. It is empty for a regular tab and when the helper reports no
// state.
func durableAttentionState(attachment base.DurableAttachment) string {
	if source, ok := attachment.(interface{ StateName() string }); ok {
		return source.StateName()
	}
	return ""
}
