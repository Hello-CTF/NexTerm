package agent

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/session"
)

// SessionRosterProbe is the production RosterRuntimeProbe: it classifies a
// handle's runtime from the real session manager, so roster presence and
// target-asset evidence come from server-side state and never from client
// claims. The probe is deliberately conservative: only a live tab with a
// connected session reports connected, a present but non-connected session
// reports disconnected, and everything else — an unknown handle, a row whose
// preassigned tab was never created or no longer exists, a foreign owner —
// reports unverifiable. Absence is never upgraded to gone: the session
// manager cannot authoritatively distinguish a runtime that never started
// from one that vanished, so it never writes that evidence.
type SessionRosterProbe struct {
	store    RosterStore
	sessions *session.Manager
}

func NewSessionRosterProbe(store RosterStore, sessions *session.Manager) *SessionRosterProbe {
	return &SessionRosterProbe{store: store, sessions: sessions}
}

func (p *SessionRosterProbe) ProbeRosterRuntime(ctx context.Context, handle, incarnation string) (RosterRuntimeStatus, error) {
	row, err := p.store.AgentRosterGetByHandle(ctx, handle)
	if err != nil {
		// A handle without an owner-visible row has nothing authoritative to
		// probe; this is also the cross-owner path, which fails closed here.
		return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable}, nil
	}
	return p.probeTab(ctx, row.TabID), nil
}

func (p *SessionRosterProbe) probeTab(ctx context.Context, tabID string) RosterRuntimeStatus {
	tab, err := p.sessions.TabFor(ctx, tabID)
	if err != nil {
		return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable}
	}
	// The tab's own exit state is authoritative and comes first: a runtime
	// that exited (EndRemote or a plain pty exit) never answers connected,
	// even while its session record still reads connected.
	if tab.Info().Exited {
		return RosterRuntimeStatus{Kind: RosterRuntimeExited}
	}
	connected, err := p.sessions.SessionFor(ctx, tab.SessionID)
	if err != nil {
		return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable}
	}
	if connected.Info().Status == session.StatusConnected {
		return RosterRuntimeStatus{Kind: RosterRuntimeConnected}
	}
	return RosterRuntimeStatus{Kind: RosterRuntimeDisconnected}
}

// ResolveRosterAsset resolves the real target asset identity behind the
// handle's runtime from the connected session. An unresolvable runtime
// returns an error; callers surface that as unknown evidence rather than
// substituting a client-claimed identity.
func (p *SessionRosterProbe) ResolveRosterAsset(ctx context.Context, handle string) (string, error) {
	row, err := p.store.AgentRosterGetByHandle(ctx, handle)
	if err != nil {
		return "", err
	}
	tab, err := p.sessions.TabFor(ctx, row.TabID)
	if err != nil {
		return "", err
	}
	connected, err := p.sessions.SessionFor(ctx, tab.SessionID)
	if err != nil {
		return "", err
	}
	return connected.Asset().ID, nil
}
