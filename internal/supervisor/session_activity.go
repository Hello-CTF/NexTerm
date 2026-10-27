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

// StateName is the session's attention state without its events; empty
// when state detection is disabled.
func (a *Attachment) StateName() string {
	s := a.session
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state != nil {
		return s.state.name()
	}
	if state := s.stateInfoLocked(false, 0); state != nil {
		return state.State
	}
	return ""
}

func (t *stateTracker) name() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// StateName is the latest attention state the helper pushed, without the
// events SessionState copies; empty when the helper reports none.
func (s *Stream) StateName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return ""
	}
	return s.state.State
}
