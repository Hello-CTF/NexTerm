package session

import "context"

// RosterRuntimeIncarnation reports the identity of the tab's current runtime
// episode so roster presence and asset rows can be tied to the episode that
// actually produced them. A durable tab reports the supervisor-daemon-assigned
// durableIdentity.Incarnation, which stays stable across client and manager
// restarts for as long as the daemon terminal lives; every other tab reports
// a unique episode minted at registration, so a removed and recreated tab —
// or a restarted manager — never aliases two episodes. The boolean is false
// when the tab does not exist for the caller (wrong owner included) or the
// episode cannot be evidenced: a durable tab whose daemon supplied no
// incarnation has no manager-side stand-in.
func (m *Manager) RosterRuntimeIncarnation(ctx context.Context, tabID string) (string, bool) {
	tab, err := m.TabFor(ctx, tabID)
	if err != nil {
		return "", false
	}
	tab.mu.Lock()
	defer tab.mu.Unlock()
	if tab.durableIdentity.Incarnation != "" {
		return tab.durableIdentity.Incarnation, true
	}
	if tab.runtimeEpisode != "" {
		return tab.runtimeEpisode, true
	}
	return "", false
}
