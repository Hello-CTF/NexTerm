package supervisor

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal/shellintegr"
)

// listOptions selects the events a list reply carries.
type listOptions struct {
	// Events includes each session's retained events with a sequence above
	// EventsAfter. Sequences are shared by all sessions of one helper
	// generation, so one value serves every session.
	Events      bool   `json:"events,omitempty"`
	EventsAfter uint64 `json:"events_after,omitempty"`
}

// listEventBudget bounds the encoded events of one list reply so it stays
// well inside a frame together with the rest of the session infos.
const listEventBudget = maxFramePayload / 2

// trimListEvents drops the oldest events across sessions until the encoded
// events fit budget, marking each session that lost events.
func trimListEvents(infos []Info, budget int) {
	type ref struct {
		info int
		seq  uint64
		size int
	}
	var refs []ref
	total := 0
	for i := range infos {
		state := infos[i].State
		if state == nil {
			continue
		}
		for _, event := range state.Events {
			size := budget + 1
			if encoded, err := json.Marshal(event); err == nil {
				size = len(encoded) + 1
			}
			refs = append(refs, ref{info: i, seq: event.Seq, size: size})
			total += size
		}
	}
	if total <= budget {
		return
	}
	// Sequences order events across sessions, and each session's events
	// are already oldest first, so dropping in sequence order removes a
	// prefix of every session's list.
	sort.Slice(refs, func(i, j int) bool { return refs[i].seq < refs[j].seq })
	drop := make(map[int]int)
	for _, r := range refs {
		if total <= budget {
			break
		}
		total -= r.size
		drop[r.info]++
	}
	for i, count := range drop {
		state := infos[i].State
		state.Events = append([]SessionEvent(nil), state.Events[count:]...)
		state.EventsDropped = true
	}
}

// SessionState returns the attachment's session state with the retained
// events after the given sequence; nil when state detection is disabled.
func (a *Attachment) SessionState(after uint64) *SessionState {
	s := a.session
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stateInfoLocked(true, after)
}

// StateUpdates is signalled when the session's attention state, prompt or
// events change. Signals coalesce; read SessionState after each one.
func (a *Attachment) StateUpdates() <-chan struct{} {
	return a.stateNotify
}

// MarkStateSeen marks the needs_me or just_finished state that began at
// since as seen by a user; every attached client is told. A state that has
// changed since is left alone. It fails with ErrStateSeenUnsupported when state
// detection is disabled.
func (a *Attachment) MarkStateSeen(since time.Time) error {
	s := a.session
	s.mu.RLock()
	tracker := s.state
	s.mu.RUnlock()
	if tracker == nil {
		return fmt.Errorf("%w: session state detection is disabled", ErrStateSeenUnsupported)
	}
	tracker.markSeen(since)
	return nil
}

// interactiveShell reports whether command starts an interactive shell,
// whose own process group in the terminal foreground means the shell reads
// the input itself.
func interactiveShell(command []string) bool {
	if len(command) == 0 || !interactiveShellArgs(command[1:]) {
		return false
	}
	if _, err := shellintegr.Detect(command[0]); err == nil {
		return true
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(command[0])), ".exe")
	switch strings.TrimPrefix(name, "-") {
	case "sh", "ash", "dash", "ksh", "mksh", "csh", "tcsh", "pwsh", "powershell", "nu", "elvish", "xonsh":
		return true
	}
	return false
}

// appCursor reports DECCKM (application cursor keys) at the end of the
// recorded output.
func (h *history) appCursor() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.modes.AppCursor
}

// TapPrompt answers the session's detected prompt with option when the
// prompt shown to the user, identified by epoch and fingerprint, is still
// on screen and tappable, and returns the bytes written. Any mismatch fails
// with ErrStalePrompt and writes nothing; a valid tap consumes the epoch.
// It fails with ErrUnsupported when state detection is disabled.
func (a *Attachment) TapPrompt(epoch uint64, fingerprint string, option int) (string, error) {
	if a.closed() {
		return "", ErrClosed
	}
	s := a.session
	s.mu.RLock()
	tracker := s.state
	s.mu.RUnlock()
	if tracker == nil {
		return "", fmt.Errorf("%w: session state detection is disabled", ErrUnsupported)
	}
	send, err := tracker.tap(epoch, fingerprint, option)
	if err != nil {
		return "", err
	}
	if _, err := s.writeInputCtx(a.ctx, []byte(send), true); err != nil {
		return "", err
	}
	return send, nil
}
