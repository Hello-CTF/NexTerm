package agent

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

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
	// snapshot reads the tab's same-source runtime snapshot; tests override
	// it to drive replacements between the reads deterministically.
	snapshot func(context.Context, string) (session.RosterRuntimeSnapshot, bool)
}

func NewSessionRosterProbe(store RosterStore, sessions *session.Manager) *SessionRosterProbe {
	return &SessionRosterProbe{
		store:    store,
		sessions: sessions,
		snapshot: sessions.RosterRuntimeSnapshot,
	}
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
// binding, reading the session package's same-source runtime snapshot so the
// session identity, the episode evidence, the exited flag, the connectivity
// status, and the asset all come from one critical section:
//  1. The tab's own exit state is authoritative and comes first (the
//     preserved exited contract): an exited runtime reports exited
//     regardless of the binding.
//  2. An unevidenced episode fails closed to unverifiable, never to
//     replaced.
//  3. A recorded binding that no longer matches is a proven replacement:
//     replaced carries the snapshot's episode as authoritative evidence,
//     never a fabricated one.
//  4. A matching (or not-yet-recorded) binding classifies the live state,
//     and the snapshot is re-read afterwards: a replacement landing during
//     the read fails closed instead of answering from two runtimes.
func (p *SessionRosterProbe) probeRow(ctx context.Context, row store.AgentRosterRow) RosterRuntimeStatus {
	snapshot, ok := p.snapshot(ctx, row.TabID)
	if !ok {
		return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable}
	}
	if snapshot.Exited {
		return RosterRuntimeStatus{Kind: RosterRuntimeExited, ObservedIncarnation: snapshot.Episode}
	}
	if !snapshot.EpisodeOK {
		return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable}
	}
	recorded := rosterRecordedEpisode(row)
	if recorded != "" && recorded != snapshot.Episode {
		return RosterRuntimeStatus{Kind: RosterRuntimeReplaced, CurrentIncarnation: snapshot.Episode, ObservedIncarnation: snapshot.Episode}
	}
	kind := RosterRuntimeDisconnected
	if snapshot.Status == session.StatusConnected {
		kind = RosterRuntimeConnected
	}
	after, ok := p.snapshot(ctx, row.TabID)
	if !ok || after.Episode != snapshot.Episode || after.SessionID != snapshot.SessionID {
		return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable}
	}
	return RosterRuntimeStatus{Kind: kind, ObservedIncarnation: snapshot.Episode, BindingValid: recorded == snapshot.Episode}
}

// ResolveRosterAsset resolves the real target asset identity behind the
// handle's runtime from the snapshot's same-source asset field, and only when
// the row's recorded episode binding matches the snapshot's episode: an
// unbound row, a stale identity, or a replaced runtime fails closed, and
// callers surface that as unknown evidence rather than substituting a
// client-claimed identity. The snapshot is re-read after the resolution so a
// replacement landing mid-read never resolves the new runtime's asset.
func (p *SessionRosterProbe) ResolveRosterAsset(ctx context.Context, handle, incarnation string) (string, error) {
	row, err := p.store.AgentRosterGetByHandle(ctx, handle)
	if err != nil {
		return "", err
	}
	if incarnation != "" && row.Incarnation != incarnation {
		return "", ipc.NewError(ipc.Code("conflict"), "roster 精确身份不匹配")
	}
	snapshot, ok := p.snapshot(ctx, row.TabID)
	if !ok {
		return "", ipc.NewError(ipc.CodeNotFound, "roster 运行时无法举证")
	}
	if !snapshot.EpisodeOK {
		return "", ipc.NewError(ipc.CodeNotFound, "roster 运行时 episode 无法举证")
	}
	recorded := rosterRecordedEpisode(row)
	if recorded == "" {
		return "", ipc.NewError(ipc.Code("conflict"), "roster 缺少运行时 episode 绑定")
	}
	if recorded != snapshot.Episode {
		return "", ipc.NewError(ipc.Code("conflict"), "roster 运行时已被替换")
	}
	assetID := snapshot.Asset.ID
	after, ok := p.snapshot(ctx, row.TabID)
	if !ok || after.Episode != snapshot.Episode || after.SessionID != snapshot.SessionID {
		return "", ipc.NewError(ipc.Code("conflict"), "roster 运行时在读取期间被替换")
	}
	return assetID, nil
}
