package supervisor

import (
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// SessionActivity is the in-memory activity record of a session; see
// base.DurableActivity.
type SessionActivity = base.DurableActivity

// activityLocked reports when the session last produced output and when
// its last client detached. A session recovered from an earlier helper has
// no record of either. The caller holds s.mu.
func (s *Session) activityLocked() *SessionActivity {
	activity := &SessionActivity{Attached: len(s.attachments) > 0}
	if at := s.lastOutput.Load(); at != 0 {
		last := time.Unix(0, at)
		activity.LastOutputAt = &last
	}
	if !activity.Attached && !s.detachedAt.IsZero() {
		detached := s.detachedAt
		activity.DetachedAt = &detached
	}
	return activity
}
