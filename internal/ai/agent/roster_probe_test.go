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
	closed chan struct{}
	once   sync.Once
}

func newProbeFakeChannel() *probeFakeChannel {
	return &probeFakeChannel{closed: make(chan struct{})}
}

func (c *probeFakeChannel) Read([]byte) (int, error) { <-c.closed; return 0, io.EOF }
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

	probe := NewSessionRosterProbe(database, manager)
	status, err := probe.ProbeRosterRuntime(ctx, "handle-a", entry.Incarnation)
	if err != nil || status.Kind != RosterRuntimeConnected {
		t.Fatalf("probe status = %+v err=%v", status, err)
	}
	assetID, err := probe.ResolveRosterAsset(ctx, "handle-a")
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
	if err := manager.Disconnect(connected.Info().ID); err != nil {
		t.Fatal(err)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	entry, err := roster.Provision(ctx, "conv-1", "handle-b", "", tab.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	probe := NewSessionRosterProbe(database, manager)
	status, err := probe.ProbeRosterRuntime(ctx, "handle-b", entry.Incarnation)
	if err != nil || status.Kind != RosterRuntimeDisconnected {
		t.Fatalf("probe status = %+v err=%v", status, err)
	}
	// A disconnected runtime still carries its real asset identity.
	assetID, err := probe.ResolveRosterAsset(ctx, "handle-b")
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
	if assetID, err := probe.ResolveRosterAsset(other, "handle-a"); err == nil && assetID != "" {
		t.Fatalf("foreign owner resolved asset %q", assetID)
	}
}
