package agent

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// RosterRuntimeIncarnationReader is the real per-tab runtime incarnation
// surface provided by the session package (session.Manager): it identifies
// the runtime episode a tab currently hosts, so presence and asset evidence
// bind to one incarnation and a replaced runtime is authoritatively detected.
type RosterRuntimeIncarnationReader interface {
	RosterRuntimeIncarnation(ctx context.Context, tabID string) (string, bool)
}

// SessionRosterProbe is the production RosterRuntimeProbe: it classifies a
// handle's runtime from the real session manager, so roster presence and
// target-asset evidence come from server-side state and never from client
// claims. The probe is deliberately conservative: only a live tab whose
// episode matches the row's recorded binding reports connected, a present but
// non-connected session reports disconnected, an exited tab reports exited
// authoritatively, and everything else — an unknown handle, a row whose
// preassigned tab was never created or no longer exists, a foreign owner, a
// stale incarnation, an unevidenced or mismatched episode — reports
// unverifiable. Absence is never upgraded to gone: the session manager
// cannot authoritatively distinguish a runtime that never started from one
// that vanished, so it never writes that evidence.
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
	// The caller's saved identity must be the row's current one: a stale
	// incarnation means the row moved on, and the probe fails closed rather
	// than answering from whatever the handle now points at.
	if incarnation != "" && row.Incarnation != incarnation {
		return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable}, nil
	}
	return p.probeRow(ctx, row), nil
}

// probeRow classifies the row's runtime against the row's persisted episode
// binding:
//  1. The tab's own exit state is authoritative and comes first (the
//     preserved exited contract): an exited runtime reports exited
//     regardless of the binding.
//  2. The current episode is read from the session manager's real
//     incarnation surface; an unevidenced episode fails closed to
//     unverifiable, never to replaced.
//  3. A recorded binding that no longer matches is a proven replacement:
//     replaced carries the observed episode as authoritative evidence, never
//     a fabricated one.
//  4. A matching (or not-yet-recorded) binding classifies the live state,
//     and the episode is re-read afterwards: a replacement landing during
//     the read fails closed instead of answering from two episodes.
func (p *SessionRosterProbe) probeRow(ctx context.Context, row store.AgentRosterRow) RosterRuntimeStatus {
	tab, err := p.sessions.TabFor(ctx, row.TabID)
	if err != nil {
		return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable}
	}
	if tab.Info().Exited {
		return RosterRuntimeStatus{Kind: RosterRuntimeExited, ObservedIncarnation: p.observedEpisode(ctx, row.TabID)}
	}
	episode, ok := p.observedEpisodeOk(ctx, row.TabID)
	if !ok {
		return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable}
	}
	recorded := rosterRecordedEpisode(row)
	if recorded != "" && recorded != episode {
		return RosterRuntimeStatus{Kind: RosterRuntimeReplaced, CurrentIncarnation: episode, ObservedIncarnation: episode}
	}
	connected, err := p.sessions.SessionFor(ctx, tab.SessionID)
	if err != nil {
		return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable, ObservedIncarnation: episode}
	}
	kind := RosterRuntimeDisconnected
	if connected.Info().Status == session.StatusConnected {
		kind = RosterRuntimeConnected
	}
	after, ok := p.observedEpisodeOk(ctx, row.TabID)
	if !ok || after != episode {
		return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable}
	}
	return RosterRuntimeStatus{Kind: kind, ObservedIncarnation: episode, BindingValid: recorded == episode}
}

// ResolveRosterAsset resolves the real target asset identity behind the
// handle's runtime from the connected session, and only when the row's
// recorded episode binding matches the currently evidenced episode: an
// unbound row, a stale identity, or a replaced runtime fails closed, and
// callers surface that as unknown evidence rather than substituting a
// client-claimed identity. The episode is re-read after the resolution so a
// replacement landing mid-read never resolves the new runtime's asset.
func (p *SessionRosterProbe) ResolveRosterAsset(ctx context.Context, handle, incarnation string) (string, error) {
	row, err := p.store.AgentRosterGetByHandle(ctx, handle)
	if err != nil {
		return "", err
	}
	if incarnation != "" && row.Incarnation != incarnation {
		return "", ipc.NewError(ipc.Code("conflict"), "roster 精确身份不匹配")
	}
	episode, ok := p.observedEpisodeOk(ctx, row.TabID)
	if !ok {
		return "", ipc.NewError(ipc.CodeNotFound, "roster 运行时 episode 无法举证")
	}
	recorded := rosterRecordedEpisode(row)
	if recorded == "" {
		return "", ipc.NewError(ipc.Code("conflict"), "roster 缺少运行时 episode 绑定")
	}
	if recorded != episode {
		return "", ipc.NewError(ipc.Code("conflict"), "roster 运行时已被替换")
	}
	tab, err := p.sessions.TabFor(ctx, row.TabID)
	if err != nil {
		return "", err
	}
	connected, err := p.sessions.SessionFor(ctx, tab.SessionID)
	if err != nil {
		return "", err
	}
	assetID := connected.Asset().ID
	after, ok := p.observedEpisodeOk(ctx, row.TabID)
	if !ok || after != episode {
		return "", ipc.NewError(ipc.Code("conflict"), "roster 运行时在读取期间被替换")
	}
	return assetID, nil
}

// observedEpisode reads the tab's current runtime episode from the session
// manager's incarnation surface, empty when the surface is unavailable or the
// episode cannot be evidenced.
func (p *SessionRosterProbe) observedEpisode(ctx context.Context, tabID string) string {
	episode, _ := p.observedEpisodeOk(ctx, tabID)
	return episode
}

// observedEpisodeOk is observedEpisode with the evidence flag.
func (p *SessionRosterProbe) observedEpisodeOk(ctx context.Context, tabID string) (string, bool) {
	reader, ok := any(p.sessions).(RosterRuntimeIncarnationReader)
	if !ok {
		return "", false
	}
	return reader.RosterRuntimeIncarnation(ctx, tabID)
}
