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

// ErrQuietSnapshot reports that the roster or a tab's quiet state kept
// changing across the consistency reads: the snapshot never returns a
// roster stitched from different incarnations or versions.
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

const (
	// quietGroupSnapshotAttempts bounds whole-snapshot retries while the
	// roster keeps changing under the reader.
	quietGroupSnapshotAttempts = 3
	// quietStateSpins bounds per-tab re-reads while the terminal keeps
	// advancing its version.
	quietStateSpins = 8
)

// QuietGroupSnapshot reads the quiet state of the requested tabs in one
// snapshot with per-tab versions. The roster is exact: the response carries
// exactly one state per requested tab ID, in request order — every
// requested tab present exactly once, no extras — or a non-nil error. Each
// tab's closed fact is read inside a version bracket (two matching
// QuietState reads), so a close landing mid-read is either fully visible
// or retried, never stitched onto a stale version; and the whole roster is
// re-verified against the manager's current incarnations before returning,
// so a tab removed and recreated under the same ID is re-resolved or fails
// the snapshot instead of reporting the removed incarnation's state.
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
	var lastErr error
	for attempt := 0; attempt < quietGroupSnapshotAttempts; attempt++ {
		states, tabs, err := m.quietGroupSnapshotOnce(ctx, tabIDs)
		if err != nil {
			lastErr = err
			continue
		}
		m.mu.Lock()
		current := true
		for index, tabID := range tabIDs {
			if m.tabs[tabID] != tabs[index] {
				current = false
				break
			}
		}
		m.mu.Unlock()
		if current {
			return states, nil
		}
		lastErr = ErrQuietSnapshot
	}
	return nil, lastErr
}

// quietGroupSnapshotOnce resolves the roster and reads every tab's quiet
// state with a per-tab version bracket.
func (m *Manager) quietGroupSnapshotOnce(ctx context.Context, tabIDs []string) ([]TabQuietState, []*Tab, error) {
	tabs := make([]*Tab, 0, len(tabIDs))
	for _, tabID := range tabIDs {
		tab, err := m.TabFor(ctx, tabID)
		if err != nil {
			return nil, nil, err
		}
		tabs = append(tabs, tab)
	}
	states := make([]TabQuietState, 0, len(tabIDs))
	for _, tab := range tabs {
		state, err := m.tabQuietState(tab)
		if err != nil {
			return nil, nil, err
		}
		states = append(states, state)
	}
	return states, tabs, nil
}

// tabQuietState reads one tab's quiet state so the closed fact and the
// version come from the same state: the terminal is sampled, the manager's
// closed/destroying flags are read, and the terminal is sampled again — the
// reading is only adopted when both versions match, otherwise it is retried
// a bounded number of times.
func (m *Manager) tabQuietState(tab *Tab) (TabQuietState, error) {
	source, ok := tab.terminal.(interface{ QuietState() terminal.TabQuietState })
	if !ok {
		return TabQuietState{}, ErrUnsupported
	}
	for spin := 0; spin < quietStateSpins; spin++ {
		before := source.QuietState()
		tab.mu.Lock()
		closed := tab.closed || tab.destroying
		tab.mu.Unlock()
		after := source.QuietState()
		if before.Version == after.Version {
			return TabQuietState{
				TabID:   tab.ID,
				IdleMS:  after.IdleMS,
				Version: after.Version,
				Closed:  closed || after.Closed,
			}, nil
		}
	}
	return TabQuietState{}, ErrQuietSnapshot
}
