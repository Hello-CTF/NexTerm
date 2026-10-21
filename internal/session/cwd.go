package session

import (
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/terminal/shellintegr"
)

// observeCWD scans tab output for OSC 7 cwd reports. Callers must hold the
// tab feed gate: the tracker is not safe for concurrent use. The generation
// is the feed's own, checked again while holding tab.mu: a replacement that
// commits while a report is being parsed resets the tracker with the new
// episode, so the old runtime's directory can never leak into it.
func (t *Tab) observeCWD(data []byte, generation uint64) (string, bool) {
	t.mu.Lock()
	if t.closed || t.generation != generation {
		t.mu.Unlock()
		return "", false
	}
	tracker := t.cwdTracker
	t.mu.Unlock()
	cwd, ok := tracker.Observe(data)
	if !ok {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.generation != generation || t.cwdTracker != tracker {
		return "", false
	}
	changed := t.cwd != cwd
	t.cwd = cwd
	return cwd, changed
}

// advanceRuntimeEpisodeLocked mints the replacement runtime's episode and
// resets the cwd evidence with it: the previous runtime's OSC 7 reports never
// describe the replacement, so the directory starts honestly unknown until
// the new runtime reports its own. Durable tabs keep their episode (and cwd):
// the daemon runtime is the same one across the reconnect. Callers must hold
// tab.mu.
func (t *Tab) advanceRuntimeEpisodeLocked() {
	t.runtimeEpisode = ids.New()
	t.cwd = ""
	t.cwdTracker = shellintegr.NewTracker()
}
