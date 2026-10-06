package session

import "github.com/ProbiusOfficial/NexTerm/internal/terminal/shellintegr"

// observeCommand scans tab output for OSC 133 command lifecycle reports and
// stores the latest snapshot. Callers must hold the tab feed gate: the
// tracker is not safe for concurrent use. The recording, replay, and
// subscriber bytes are untouched.
func (t *Tab) observeCommand(data []byte) {
	state, _ := t.commandTrack.Observe(data)
	t.mu.Lock()
	t.commandState = state
	t.mu.Unlock()
}

// CommandState returns the latest OSC 133 command lifecycle snapshot for the
// tab: whether a command is running, how many have started, and the last
// reported exit code.
func (t *Tab) CommandState() shellintegr.CommandState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commandState
}
