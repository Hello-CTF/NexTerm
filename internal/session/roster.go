package session

import "context"

// RosterRuntimeSnapshot captures every roster-relevant runtime field of a tab
// from a single critical section, so a caller never combines one tab's
// session state with another tab's episode: tab and session identity, the
// runtime episode evidence, the tab's exited flag, and the session's
// connectivity status and current asset all come from the same read, even
// when a reconnect commits or the tab is removed and recreated concurrently.
type RosterRuntimeSnapshot struct {
	TabID     string
	SessionID string
	Episode   string
	// EpisodeOK is false when the runtime episode cannot be evidenced (a
	// durable tab whose daemon supplied no incarnation): callers must fail
	// closed instead of matching the empty episode.
	EpisodeOK bool
	Exited    bool
	Status    Status
	Asset     Asset
}

// RosterRuntimeSnapshot returns the tab's same-source runtime snapshot. The
// boolean is false when the tab does not exist for the caller (wrong owner
// included).
func (m *Manager) RosterRuntimeSnapshot(ctx context.Context, tabID string) (RosterRuntimeSnapshot, bool) {
	tab, err := m.TabFor(ctx, tabID)
	if err != nil {
		return RosterRuntimeSnapshot{}, false
	}
	session := tab.session
	if session == nil {
		return RosterRuntimeSnapshot{}, false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	tab.mu.Lock()
	defer tab.mu.Unlock()
	episode := tab.durableIdentity.Incarnation
	if episode == "" {
		episode = tab.runtimeEpisode
	}
	return RosterRuntimeSnapshot{
		TabID:     tab.ID,
		SessionID: session.ID,
		Episode:   episode,
		EpisodeOK: episode != "",
		Exited:    tab.exited,
		Status:    session.status,
		Asset:     session.Asset(),
	}, true
}

// RosterRuntimeIncarnation reports the identity of the tab's current runtime
// episode so roster presence and asset rows can be tied to the episode that
// actually produced them. A durable tab reports the supervisor-daemon-assigned
// durableIdentity.Incarnation, which stays stable across client and manager
// restarts for as long as the daemon terminal lives; every other tab reports
// a unique episode minted at registration and advanced whenever a reconnect
// commits a replacement runtime, so a removed and recreated tab — or a
// restarted manager — never aliases two episodes. The boolean is false when
// the tab does not exist for the caller (wrong owner included) or the episode
// cannot be evidenced: a durable tab whose daemon supplied no incarnation has
// no manager-side stand-in.
func (m *Manager) RosterRuntimeIncarnation(ctx context.Context, tabID string) (string, bool) {
	snapshot, ok := m.RosterRuntimeSnapshot(ctx, tabID)
	if !ok || !snapshot.EpisodeOK {
		return "", false
	}
	return snapshot.Episode, true
}
