package agent

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// The episode fixture drives a real session manager with a fake transport so
// tabs can be removed and recreated with the same ID — the exact replacement
// shape a roster row must not survive as "connected".
func newEpisodeFixture(t *testing.T) (*store.Store, *session.Manager, context.Context) {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	transport := &probeFakeTransport{}
	manager := session.NewManager(session.Config{
		IdleTimeout: -1,
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	return database, manager, ipc.WithUserID(context.Background(), "owner-a")
}

func openEpisodeTab(t *testing.T, manager *session.Manager, ctx context.Context, sessionID, tabID string) string {
	t.Helper()
	tab, err := manager.OpenTab(ctx, session.OpenTabOptions{SessionID: sessionID, TabID: tabID, ClientID: "client-" + tabID, ChannelID: "channel-" + tabID, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	episode, ok := manager.RosterRuntimeIncarnation(ctx, tab.ID)
	if !ok || episode == "" {
		t.Fatalf("tab %s has no evidenced runtime episode", tab.ID)
	}
	return episode
}

// A roster row is bound to the runtime episode its first successful attach
// found; a replaced runtime (tab removed and recreated with the same ID) must
// never be reported connected nor resolve the replacement's asset — the probe
// answers replaced with the authoritatively observed episode, or fails closed
// when the episode cannot be evidenced.
func TestRosterEpisodeBindingBlocksRecreatedRuntime(t *testing.T) {
	database, manager, ctx := newEpisodeFixture(t)
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-1", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	episodeA := openEpisodeTab(t, manager, ctx, connected.Info().ID, "tab-ep")
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	entry, err := roster.Provision(ctx, "conv-1", "handle-ep", "", "tab-ep", nil)
	if err != nil {
		t.Fatal(err)
	}

	// The first attach binds the row to the episode it found.
	attachment, err := roster.Attach(ctx, "handle-ep", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionConnected {
		t.Fatalf("first attach = %+v", attachment)
	}
	row, err := database.AgentRosterGetByHandle(ctx, "handle-ep")
	if err != nil {
		t.Fatal(err)
	}
	if bound := rosterRecordedEpisode(row); bound != episodeA {
		t.Fatalf("bound episode = %q, want %q", bound, episodeA)
	}

	// Replace the runtime: remove the tab and recreate it with the same ID.
	if err := manager.CloseTab("tab-ep"); err != nil {
		t.Fatal(err)
	}
	episodeB := openEpisodeTab(t, manager, ctx, connected.Info().ID, "tab-ep")
	if episodeB == episodeA {
		t.Fatal("recreated tab aliases the old episode")
	}

	probe := NewSessionRosterProbe(database, manager)
	status, err := probe.ProbeRosterRuntime(ctx, "handle-ep", entry.Incarnation)
	if err != nil {
		t.Fatal(err)
	}
	if status.Kind == RosterRuntimeConnected {
		t.Fatalf("stale row reported the replacement runtime as connected: %+v", status)
	}
	if status.Kind == RosterRuntimeReplaced && status.CurrentIncarnation != episodeB {
		t.Fatalf("replaced evidence = %+v, want the observed episode %q", status, episodeB)
	}
	if status.Kind != RosterRuntimeReplaced && status.Kind != RosterRuntimeUnverifiable {
		t.Fatalf("probe status = %+v, want replaced or unverifiable", status)
	}
	if assetID, err := probe.ResolveRosterAsset(ctx, "handle-ep", entry.Incarnation); err == nil {
		t.Fatalf("stale row resolved the replacement runtime's asset %q", assetID)
	}

	// Attach resolves the replacement authoritatively instead of binding it.
	replacement, err := roster.Attach(ctx, "handle-ep", "job-2")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Resolution == RosterResolutionConnected {
		t.Fatalf("attach bound the replacement runtime: %+v", replacement)
	}
	if replacement.Resolution == RosterResolutionReplaced && replacement.CurrentIncarnation != episodeB {
		t.Fatalf("attach replacement evidence = %+v, want %q", replacement, episodeB)
	}
}

// The binding keeps working while the episode is unchanged: presence reports
// connected and the asset resolves, and a second attach rebinds the job.
func TestRosterEpisodeBindingHoldsWhileEpisodeUnchanged(t *testing.T) {
	database, manager, ctx := newEpisodeFixture(t)
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-1", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	episodeA := openEpisodeTab(t, manager, ctx, connected.Info().ID, "tab-ep")
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	entry, err := roster.Provision(ctx, "conv-1", "handle-ep", "", "tab-ep", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roster.Attach(ctx, "handle-ep", "job-1"); err != nil {
		t.Fatal(err)
	}

	probe := NewSessionRosterProbe(database, manager)
	status, err := probe.ProbeRosterRuntime(ctx, "handle-ep", entry.Incarnation)
	if err != nil || status.Kind != RosterRuntimeConnected || !status.BindingValid || status.ObservedIncarnation != episodeA {
		t.Fatalf("probe status = %+v err=%v, want the bound episode %q", status, err, episodeA)
	}
	if assetID, err := probe.ResolveRosterAsset(ctx, "handle-ep", entry.Incarnation); err != nil || assetID != "asset-1" {
		t.Fatalf("resolved asset = %q err=%v", assetID, err)
	}
	rebound, err := roster.Attach(ctx, "handle-ep", "job-2")
	if err != nil || rebound.Resolution != RosterResolutionConnected || rebound.Entry.JobID != "job-2" {
		t.Fatalf("rebind = %+v err=%v", rebound, err)
	}
}

// A rebuilt manager mints fresh episodes: a row bound under the old manager
// must not report connected under the new one.
func TestRosterEpisodeBindingFailsClosedAcrossManagerRebuild(t *testing.T) {
	database, manager, ctx := newEpisodeFixture(t)
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-1", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	openEpisodeTab(t, manager, ctx, connected.Info().ID, "tab-ep")
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	entry, err := roster.Provision(ctx, "conv-1", "handle-ep", "", "tab-ep", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roster.Attach(ctx, "handle-ep", "job-1"); err != nil {
		t.Fatal(err)
	}

	rebuilt := session.NewManager(session.Config{
		IdleTimeout: -1,
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return &probeFakeTransport{}, nil
		}),
	})
	t.Cleanup(func() { _ = rebuilt.Close() })
	reconnected, err := rebuilt.ConnectFor(ctx, session.Asset{ID: "asset-1", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	episodeB := openEpisodeTab(t, rebuilt, ctx, reconnected.Info().ID, "tab-ep")

	probe := NewSessionRosterProbe(database, rebuilt)
	status, err := probe.ProbeRosterRuntime(ctx, "handle-ep", entry.Incarnation)
	if err != nil {
		t.Fatal(err)
	}
	if status.Kind == RosterRuntimeConnected {
		t.Fatalf("row bound under the old manager reported connected after rebuild: %+v", status)
	}
	if status.Kind == RosterRuntimeReplaced && status.CurrentIncarnation != episodeB {
		t.Fatalf("rebuild evidence = %+v, want %q", status, episodeB)
	}
	if assetID, err := probe.ResolveRosterAsset(ctx, "handle-ep", entry.Incarnation); err == nil {
		t.Fatalf("row bound under the old manager resolved the rebuilt runtime's asset %q", assetID)
	}
}

// Without a recorded binding there is nothing to validate against: presence
// fails closed to unverifiable and asset evidence stays unknown — a row can
// only claim the runtime its recorded episode matches.
func TestRosterEpisodeBindingFailsClosedWithoutBinding(t *testing.T) {
	database, manager, ctx := newEpisodeFixture(t)
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-1", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	openEpisodeTab(t, manager, ctx, connected.Info().ID, "tab-ep")
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	entry, err := roster.Provision(ctx, "conv-1", "handle-ep", "", "tab-ep", nil)
	if err != nil {
		t.Fatal(err)
	}

	probe := NewSessionRosterProbe(database, manager)
	status, err := probe.ProbeRosterRuntime(ctx, "handle-ep", entry.Incarnation)
	if err != nil || status.Kind != RosterRuntimeConnected || status.BindingValid {
		t.Fatalf("unbound probe status = %+v err=%v, want connected without a valid binding", status, err)
	}
	// The list path refuses the unproven claim: presence is unverifiable.
	runner := NewRunner(Config{Store: database, Roster: roster})
	t.Cleanup(func() { _ = runner.Close() })
	if presence := runner.rosterPresence(ctx, entry); presence != RosterResolutionUnverifiable {
		t.Fatalf("unbound presence = %q, want unverifiable", presence)
	}
	if _, err := probe.ResolveRosterAsset(ctx, "handle-ep", entry.Incarnation); err == nil {
		t.Fatal("unbound row resolved an asset")
	}
}
