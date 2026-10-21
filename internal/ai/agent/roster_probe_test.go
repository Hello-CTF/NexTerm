package agent

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func newRosterProbeFixture(t *testing.T) (*store.Store, *session.Manager) {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager := session.NewManager(session.Config{IdleTimeout: -1})
	t.Cleanup(func() { _ = manager.Close() })
	return database, manager
}

type probeFakeChannel struct {
	closed   chan struct{}
	once     sync.Once
	incoming chan []byte
}

func newProbeFakeChannel() *probeFakeChannel {
	return &probeFakeChannel{closed: make(chan struct{}), incoming: make(chan []byte, 16)}
}

func (c *probeFakeChannel) Read(p []byte) (int, error) {
	select {
	case data := <-c.incoming:
		return copy(p, data), nil
	case <-c.closed:
		return 0, io.EOF
	}
}

// emit injects runtime output into the channel, driving the same pump path a
// real transport read would.
func (c *probeFakeChannel) emit(data []byte) {
	select {
	case c.incoming <- data:
	case <-c.closed:
	}
}

func (c *probeFakeChannel) Write(data []byte) (int, error) {
	return len(data), nil
}
func (c *probeFakeChannel) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
func (c *probeFakeChannel) CloseWrite() error                            { return nil }
func (c *probeFakeChannel) Stderr() io.Reader                            { return strings.NewReader("") }
func (c *probeFakeChannel) Resize(context.Context, uint32, uint32) error { return nil }
func (c *probeFakeChannel) Wait(context.Context) error                   { <-c.closed; return nil }
func (c *probeFakeChannel) ID() string                                   { return "probe-fake" }
func (c *probeFakeChannel) Generation() uint64                           { return 1 }

type probeFakeTransport struct {
	mu       sync.Mutex
	channels []*probeFakeChannel
}

func (t *probeFakeTransport) Kind() string       { return "ssh" }
func (t *probeFakeTransport) Generation() uint64 { return 1 }
func (t *probeFakeTransport) IsAlive() bool      { return true }
func (t *probeFakeTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *probeFakeTransport) Close() error { return nil }
func (t *probeFakeTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}
func (t *probeFakeTransport) OpenPTY(context.Context, base.PTYOptions) (base.Channel, error) {
	channel := newProbeFakeChannel()
	t.mu.Lock()
	t.channels = append(t.channels, channel)
	t.mu.Unlock()
	return channel, nil
}

func (t *probeFakeTransport) lastChannel() *probeFakeChannel {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.channels[len(t.channels)-1]
}

func TestSessionRosterProbeResolvesConnectedRuntimeAndAsset(t *testing.T) {
	database, manager := newRosterProbeFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	tab, err := manager.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-1", Title: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	entry, err := roster.Provision(ctx, "conv-1", "handle-a", "", tab.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The first attach binds the row to the runtime episode it found; only a
	// bound row may report connected or resolve the asset.
	if _, err := roster.Attach(ctx, "handle-a", "job-1"); err != nil {
		t.Fatal(err)
	}

	probe := NewSessionRosterProbe(database, manager)
	status, err := probe.ProbeRosterRuntime(ctx, "handle-a", entry.Incarnation)
	if err != nil || status.Kind != RosterRuntimeConnected || !status.BindingValid {
		t.Fatalf("probe status = %+v err=%v", status, err)
	}
	assetID, err := probe.ResolveRosterAsset(ctx, "handle-a", entry.Incarnation)
	if err != nil || assetID != "asset-1" {
		t.Fatalf("resolved asset = %q err=%v", assetID, err)
	}
}

func TestSessionRosterProbeClassifiesDisconnectedSession(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager := session.NewManager(session.Config{
		IdleTimeout: -1,
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return &probeFakeTransport{}, nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-2", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.OpenTab(ctx, session.OpenTabOptions{SessionID: connected.Info().ID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	entry, err := roster.Provision(ctx, "conv-1", "handle-b", "", tab.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Bind the row to the live episode first; the binding then survives the
	// disconnect and keeps resolving the real asset identity.
	if _, err := roster.Attach(ctx, "handle-b", "job-1"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disconnect(connected.Info().ID); err != nil {
		t.Fatal(err)
	}

	probe := NewSessionRosterProbe(database, manager)
	status, err := probe.ProbeRosterRuntime(ctx, "handle-b", entry.Incarnation)
	if err != nil || status.Kind != RosterRuntimeDisconnected || !status.BindingValid {
		t.Fatalf("probe status = %+v err=%v", status, err)
	}
	// A disconnected runtime still carries its real asset identity.
	assetID, err := probe.ResolveRosterAsset(ctx, "handle-b", entry.Incarnation)
	if err != nil || assetID != "asset-2" {
		t.Fatalf("resolved asset = %q err=%v", assetID, err)
	}
}

// A tab whose runtime has exited is authoritatively exited: EndRemote and a
// plain pty exit both forbid a connected answer, and an attach writes the
// exited tombstone instead of binding the job to a dead runtime.
func TestSessionRosterProbeClassifiesExitedTabBeforeConnected(t *testing.T) {
	t.Run("EndRemote", func(t *testing.T) {
		database, manager := newRosterProbeFixture(t)
		ctx := ipc.WithUserID(context.Background(), "owner-a")
		tab, err := manager.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-1", Title: "agent"})
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.EndRemote(tab.ID, nil); err != nil {
			t.Fatal(err)
		}
		roster := NewRoster(database, NewSessionRosterProbe(database, manager))
		entry, err := roster.Provision(ctx, "conv-1", "handle-ended", "", tab.ID, nil)
		if err != nil {
			t.Fatal(err)
		}

		probe := NewSessionRosterProbe(database, manager)
		status, err := probe.ProbeRosterRuntime(ctx, "handle-ended", entry.Incarnation)
		if err != nil || status.Kind != RosterRuntimeExited {
			t.Fatalf("probe status = %+v err=%v, want authoritative exited", status, err)
		}
		attachment, err := roster.Attach(ctx, "handle-ended", "job-1")
		if err != nil {
			t.Fatal(err)
		}
		if attachment.Resolution == RosterResolutionConnected || attachment.Entry.State != RosterStateExited {
			t.Fatalf("attach bound a dead runtime: %+v", attachment)
		}
	})

	t.Run("pty exit", func(t *testing.T) {
		ctx := ipc.WithUserID(context.Background(), "owner-a")
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
		connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-2", Kind: session.KindSSH})
		if err != nil {
			t.Fatal(err)
		}
		tab, err := manager.OpenTab(ctx, session.OpenTabOptions{SessionID: connected.Info().ID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24})
		if err != nil {
			t.Fatal(err)
		}
		if err := transport.lastChannel().Close(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			info, err := manager.TabFor(ctx, tab.ID)
			if err == nil && info.Info().Exited {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("tab did not record the pty exit")
			}
			time.Sleep(10 * time.Millisecond)
		}

		roster := NewRoster(database, NewSessionRosterProbe(database, manager))
		entry, err := roster.Provision(ctx, "conv-1", "handle-pty-exit", "", tab.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		probe := NewSessionRosterProbe(database, manager)
		status, err := probe.ProbeRosterRuntime(ctx, "handle-pty-exit", entry.Incarnation)
		if err != nil || status.Kind != RosterRuntimeExited {
			t.Fatalf("probe status = %+v err=%v, want authoritative exited", status, err)
		}
	})
}

// A stale incarnation never resolves: the caller's saved identity must match
// the row's current incarnation, and a mismatch fails closed instead of
// answering from whatever runtime the handle now points at.
func TestSessionRosterProbeFailsClosedOnStaleIncarnation(t *testing.T) {
	database, manager := newRosterProbeFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	tab, err := manager.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-1", Title: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	entry, err := roster.Provision(ctx, "conv-1", "handle-a", "", tab.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The row moves on underneath the caller: the saved identity is stale.
	if _, err := database.AgentRosterUpdate(ctx, entry.ID, store.AgentRosterIdentity{CreatedAt: entry.CreatedAt, Incarnation: entry.Incarnation}, map[string]any{"incarnation": "incarnation-2"}); err != nil {
		t.Fatal(err)
	}

	probe := NewSessionRosterProbe(database, manager)
	status, err := probe.ProbeRosterRuntime(ctx, "handle-a", entry.Incarnation)
	if err != nil || status.Kind != RosterRuntimeUnverifiable {
		t.Fatalf("stale probe status = %+v err=%v, want fail-closed unverifiable", status, err)
	}
	if assetID, err := probe.ResolveRosterAsset(ctx, "handle-a", entry.Incarnation); err == nil {
		t.Fatalf("stale incarnation resolved the asset %q", assetID)
	}

	// The row's current identity still resolves honestly, once the row is
	// bound to the live runtime episode.
	if _, err := roster.Attach(ctx, "handle-a", "job-1"); err != nil {
		t.Fatal(err)
	}
	status, err = probe.ProbeRosterRuntime(ctx, "handle-a", "incarnation-2")
	if err != nil || status.Kind != RosterRuntimeConnected || !status.BindingValid {
		t.Fatalf("current probe status = %+v err=%v", status, err)
	}
	if assetID, err := probe.ResolveRosterAsset(ctx, "handle-a", "incarnation-2"); err != nil || assetID != "asset-1" {
		t.Fatalf("current incarnation resolved %q err=%v", assetID, err)
	}
}

func TestSessionRosterProbeFailsClosedToUnverifiable(t *testing.T) {
	database, manager := newRosterProbeFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	other := ipc.WithUserID(context.Background(), "owner-b")
	tab, err := manager.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-1", Title: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	if _, err := roster.Provision(ctx, "conv-1", "handle-a", "", tab.ID, nil); err != nil {
		t.Fatal(err)
	}
	// A row whose preassigned tab was never created (or no longer exists)
	// must not be classified gone: the probe has no authoritative absence.
	if _, err := roster.Provision(ctx, "conv-1", "handle-missing-tab", "", "tab-never-created", nil); err != nil {
		t.Fatal(err)
	}

	probe := NewSessionRosterProbe(database, manager)
	for name, probeCtx := range map[string]context.Context{
		"unknown handle": ctx,
		"missing tab":    ctx,
		"foreign owner":  other,
	} {
		handle := "handle-a"
		if name == "unknown handle" {
			handle = "handle-unknown"
		}
		if name == "missing tab" {
			handle = "handle-missing-tab"
		}
		status, err := probe.ProbeRosterRuntime(probeCtx, handle, "")
		if err != nil || status.Kind != RosterRuntimeUnverifiable {
			t.Fatalf("%s: probe status = %+v err=%v", name, status, err)
		}
	}
	// Cross-owner resolution never leaks the real asset identity.
	if assetID, err := probe.ResolveRosterAsset(other, "handle-a", ""); err == nil && assetID != "" {
		t.Fatalf("foreign owner resolved asset %q", assetID)
	}
}

func rosterStringPtr(value string) *string { return &value }

// The probe's target evidence comes from verifiable snapshots only: the host
// is the bound store asset's configured host and the working directory is the
// tab's own OSC 7 report, resolved under the recorded episode binding.
func TestSessionRosterProbeResolvesTargetEvidence(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "prod-host", Host: rosterStringPtr("prod.example")})
	if err != nil {
		t.Fatal(err)
	}
	transport := &probeFakeTransport{}
	manager := session.NewManager(session.Config{
		IdleTimeout: -1,
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: asset.ID, Name: asset.Name, Kind: session.KindSSH, StoreBacked: true})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.OpenTab(ctx, session.OpenTabOptions{SessionID: connected.Info().ID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	// The runtime reports its working directory through the real pump path.
	transport.lastChannel().emit([]byte("\x1b]7;file://prod/srv/app\x07"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, ok := manager.RosterRuntimeSnapshot(ctx, tab.ID)
		if ok && snapshot.Cwd == "/srv/app" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot never carried the OSC 7 cwd")
		}
		time.Sleep(10 * time.Millisecond)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	entry, err := roster.Provision(ctx, "conv-1", "handle-a", "", tab.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Only a row bound to the live runtime episode may resolve target evidence.
	if _, err := roster.Attach(ctx, "handle-a", "job-1"); err != nil {
		t.Fatal(err)
	}

	probe := NewSessionRosterProbe(database, manager)
	evidence, err := probe.ResolveRosterTarget(ctx, "handle-a", entry.Incarnation)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Host != "prod.example" {
		t.Fatalf("host evidence = %q, want the configured store asset host", evidence.Host)
	}
	if evidence.Cwd != "/srv/app" {
		t.Fatalf("cwd evidence = %q, want the OSC 7 report", evidence.Cwd)
	}
}

// Every unverifiable input resolves to an honestly unknown host: a volatile
// inline asset, a remote shell, and a store-backed session whose asset row is
// gone all stay empty — the asset name is never substituted for a real host.
// A reported cwd still surfaces wherever the tab evidenced one.
func TestSessionRosterProbeTargetEvidenceUnknownHost(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
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
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	probe := NewSessionRosterProbe(database, manager)

	bind := func(handle, tabID string) RosterEntry {
		t.Helper()
		entry, err := roster.Provision(ctx, "conv-1", handle, "", tabID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := roster.Attach(ctx, handle, "job-1"); err != nil {
			t.Fatal(err)
		}
		return entry
	}

	// A remote session shell is not store-backed: no host is verifiable, but
	// its OSC 7 cwd still is.
	remote, err := manager.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-remote", Title: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.FeedRemote(ctx, remote.ID, []byte("\x1b]7;file://shell/srv/remote\x07")); err != nil {
		t.Fatal(err)
	}
	remoteEntry := bind("handle-remote", remote.ID)
	evidence, err := probe.ResolveRosterTarget(ctx, "handle-remote", remoteEntry.Incarnation)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Host != "" {
		t.Fatalf("remote shell host = %q, want unknown", evidence.Host)
	}
	if evidence.Cwd != "/srv/remote" {
		t.Fatalf("remote shell cwd = %q, want the OSC 7 report", evidence.Cwd)
	}

	// An inline (volatile) SSH asset never consults the store: unknown host.
	inline, err := manager.ConnectFor(ctx, session.Asset{ID: "inline-asset", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	inlineTab, err := manager.OpenTab(ctx, session.OpenTabOptions{SessionID: inline.Info().ID, ClientID: "client", ChannelID: "channel-inline", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	inlineEntry := bind("handle-inline", inlineTab.ID)
	evidence, err = probe.ResolveRosterTarget(ctx, "handle-inline", inlineEntry.Incarnation)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Host != "" || evidence.Cwd != "" {
		t.Fatalf("inline asset evidence = %+v, want both unknown", evidence)
	}

	// A store-backed session whose asset row no longer exists resolves no
	// host rather than echoing the session's asset metadata.
	missing, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-deleted", Name: "deleted-name", Kind: session.KindSSH, StoreBacked: true})
	if err != nil {
		t.Fatal(err)
	}
	missingTab, err := manager.OpenTab(ctx, session.OpenTabOptions{SessionID: missing.Info().ID, ClientID: "client", ChannelID: "channel-missing", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	missingEntry := bind("handle-missing", missingTab.ID)
	evidence, err = probe.ResolveRosterTarget(ctx, "handle-missing", missingEntry.Incarnation)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Host != "" {
		t.Fatalf("deleted asset host = %q, want unknown (never the asset name %q)", evidence.Host, "deleted-name")
	}
}

// Target evidence follows the same fail-closed identity contract as the asset
// evidence: stale incarnations, foreign owners, and unbound rows resolve
// nothing.
func TestSessionRosterProbeTargetEvidenceFailsClosed(t *testing.T) {
	database, manager := newRosterProbeFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	foreign := ipc.WithUserID(context.Background(), "owner-b")
	tab, err := manager.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-1", Title: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	entry, err := roster.Provision(ctx, "conv-1", "handle-a", "", tab.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	probe := NewSessionRosterProbe(database, manager)

	// An unbound row has no recorded episode binding: nothing resolves.
	if evidence, err := probe.ResolveRosterTarget(ctx, "handle-a", entry.Incarnation); err == nil {
		t.Fatalf("unbound row resolved %+v", evidence)
	}
	if _, err := roster.Attach(ctx, "handle-a", "job-1"); err != nil {
		t.Fatal(err)
	}
	// A stale saved incarnation fails closed.
	if evidence, err := probe.ResolveRosterTarget(ctx, "handle-a", "inc-stale"); err == nil {
		t.Fatalf("stale incarnation resolved %+v", evidence)
	}
	// A foreign owner never resolves the handle.
	if evidence, err := probe.ResolveRosterTarget(foreign, "handle-a", entry.Incarnation); err == nil {
		t.Fatalf("foreign owner resolved %+v", evidence)
	}
	// The current owner identity resolves (host unknown on a remote shell).
	evidence, err := probe.ResolveRosterTarget(ctx, "handle-a", entry.Incarnation)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Host != "" {
		t.Fatalf("remote shell host = %q, want unknown", evidence.Host)
	}
}
