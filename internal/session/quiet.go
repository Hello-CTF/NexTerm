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

// ErrQuietSnapshot reports that the roster changed during the read: the
// snapshot never returns a roster stitched from different incarnations.
var ErrQuietSnapshot = errors.New("quiet group snapshot changed during the read")

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
// exactly one state per requested tab ID, in request order — every
// requested tab present exactly once, no extras — or a non-nil error. Each
// state comes from one versioned terminal read, so the version and the
// closed fact always describe the same state; the manager's own
// closed/destroying flags are deliberately excluded, so a manager teardown
// that has not reached the terminal's Close yet never reports a closed tab
// at the terminal's current version. The roster is re-verified against the
// manager's current incarnations before returning: a tab removed or
// recreated under the same ID during the read fails the whole snapshot
// instead of silently adopting the new incarnation's state.
func (m *Manager) QuietGroupSnapshot(ctx context.Context, tabIDs []string) ([]TabQuietState, error) {
	if len(tabIDs) == 0 {
		return []TabQuietState{}, nil
	}
	seen := make(map[string]struct{}, len(tabIDs))
	for _, tabID := range tabIDs {
		if _, duplicate := seen[tabID]; duplicate {
			return nil, ErrQuietRoster
		}
		seen[tabID] = struct{}{}
	}
	tabs := make([]*Tab, 0, len(tabIDs))
	for _, tabID := range tabIDs {
		tab, err := m.TabFor(ctx, tabID)
		if err != nil {
			return nil, err
		}
		tabs = append(tabs, tab)
	}
	states := make([]TabQuietState, 0, len(tabIDs))
	for _, tab := range tabs {
		state, err := m.tabQuietState(tab)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for index, tabID := range tabIDs {
		if m.tabs[tabID] != tabs[index] {
			return nil, ErrQuietSnapshot
		}
	}
	return states, nil
}

// tabQuietState reads one tab's quiet state from the terminal's versioned
// state: the version and the closed fact come from the same read, so a
// close is reported only once the terminal itself has closed and bumped
// the version.
func (m *Manager) tabQuietState(tab *Tab) (TabQuietState, error) {
	source, ok := tab.terminal.(interface{ QuietState() terminal.TabQuietState })
	if !ok {
		return TabQuietState{}, ErrUnsupported
	}
	quiet := source.QuietState()
	return TabQuietState{
		TabID:   tab.ID,
		IdleMS:  quiet.IdleMS,
		Version: quiet.Version,
		Closed:  quiet.Closed,
	}, nil
}
