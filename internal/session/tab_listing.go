package session

import (
	"math"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// detachedAtLocked is when the last subscriber left, zero while one is
// attached. The caller holds t.mu.
func (t *Tab) detachedAtLocked() int64 {
	if len(t.subscribers) > 0 {
		return 0
	}
	return unixMilliOrZero(t.detachedAt)
}

func unixMilliOrZero(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UnixMilli()
}

// durableAttentionState reads the helper's attention state from a durable
// attachment: a remote stream reports the latest pushed state and an
// in-process attachment reads it directly. It is empty for a regular tab
// and when the helper reports no state.
func durableAttentionState(attachment base.DurableAttachment) string {
	var state *base.SessionState
	switch source := attachment.(type) {
	case interface{ SessionState() *base.SessionState }:
		state = source.SessionState()
	case interface {
		SessionState(after uint64) *base.SessionState
	}:
		// The newest possible sequence leaves out the retained events.
		state = source.SessionState(math.MaxUint64)
	}
	if state == nil {
		return ""
	}
	return state.State
}
