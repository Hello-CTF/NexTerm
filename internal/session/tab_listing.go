package session

import (
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// dropSubscriberLocked removes a subscriber; when it was the last one the
// tab went to the background now. The caller holds t.mu.
func (t *Tab) dropSubscriberLocked(channelID string) {
	if _, ok := t.subscribers[channelID]; !ok {
		return
	}
	delete(t.subscribers, channelID)
	t.detaches++
	if len(t.subscribers) == 0 {
		t.detachedAt = time.Now()
	}
}

// detachedAtLocked is when the last subscriber left, zero while one is
// attached. The caller holds t.mu.
func (t *Tab) detachedAtLocked() int64 {
	if len(t.subscribers) > 0 {
		return 0
	}
	return unixMilliOrZero(t.detachedAt)
}

// detachedSinceOpenLocked reports that DetachedAt is only when this app run
// reopened an existing helper session: no client has left it since, so the
// real detach happened no later than that. The caller holds t.mu.
func (t *Tab) detachedSinceOpenLocked() bool {
	return t.reopened && t.detaches == 0 && len(t.subscribers) == 0
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
