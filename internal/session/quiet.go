package session

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// ErrQuietRoster rejects a quiet-group request that does not list each tab
// exactly once: an incomplete or duplicated roster never produces a
// snapshot.
var ErrQuietRoster = errors.New("quiet group roster must list each tab exactly once")

// TabQuietState is one tab's quiet reading at a point in the terminal's
// state: idle milliseconds since the last output, a version that bumps on
// any output, resize, mode change, encoding switch, or close, and whether
// the tab is closed. Two group snapshots whose per-tab versions all match
// describe one consistent state.
type TabQuietState struct {
	TabID   string
	IdleMS  int64
	Version uint64
	Closed  bool
}

// QuietGroupSnapshot reads the quiet state of the requested tabs in one
// snapshot with per-tab versions. The roster is exact: the response carries
// exactly one state per requested tab ID, in request order — every requested
// tab present exactly once, no extras — or a non-nil error. A tab that
// vanished before its read fails the whole snapshot; a closed tab stays in
// the roster with its closed fact visible.
func (m *Manager) QuietGroupSnapshot(ctx context.Context, tabIDs []string) ([]TabQuietState, error) {
	if len(tabIDs) == 0 {
		return []TabQuietState{}, nil
	}
	seen := make(map[string]struct{}, len(tabIDs))
	states := make([]TabQuietState, 0, len(tabIDs))
	for _, tabID := range tabIDs {
		if _, duplicate := seen[tabID]; duplicate {
			return nil, ErrQuietRoster
		}
		seen[tabID] = struct{}{}
		tab, err := m.TabFor(ctx, tabID)
		if err != nil {
			return nil, err
		}
		tab.mu.Lock()
		closed := tab.closed || tab.destroying
		terminalState := tab.terminal
		tab.mu.Unlock()
		source, ok := terminalState.(interface{ QuietState() terminal.TabQuietState })
		if !ok {
			return nil, ErrUnsupported
		}
		quiet := source.QuietState()
		states = append(states, TabQuietState{
			TabID:   tabID,
			IdleMS:  quiet.IdleMS,
			Version: quiet.Version,
			Closed:  closed || quiet.Closed,
		})
	}
	return states, nil
}
