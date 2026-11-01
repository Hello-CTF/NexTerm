package production

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func connectedViaManager(t *testing.T, owner context.Context, assetID, kind string) (*session.Manager, *dbTunnelTestTransport) {
	t.Helper()
	transport := newDBTunnelTestTransport(kind, 1)
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
		ReconnectBackoff: []time.Duration{0},
		IdleTimeout:      -1,
	})
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.Connect(owner, session.Asset{ID: assetID, Kind: kind}); err != nil {
		t.Fatal(err)
	}
	return manager, transport
}

func TestSessionAssetDialerReachesDeviceSessionsOnlyWhenAllowed(t *testing.T) {
	database := newDBTunnelTestStore(t)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	manager, transport := connectedViaManager(t, owner, "device/dev-1", session.KindDevice)

	dial, err := newSessionAssetDialer(database, manager, true)(owner, "device/dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dial(t.Context(), "tcp", "10.0.0.5:3389"); !errors.Is(err, errDBTunnelTestDial) {
		t.Fatalf("device dial = %v", err)
	}
	transport.mu.Lock()
	if len(transport.addresses) != 1 || transport.addresses[0] != "10.0.0.5:3389" {
		t.Fatalf("addresses = %v", transport.addresses)
	}
	transport.mu.Unlock()

	// Another account cannot borrow the device session.
	foreign := ipc.WithUserID(t.Context(), "owner-b")
	foreignDial, err := newSessionAssetDialer(database, manager, true)(foreign, "device/dev-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = foreignDial(t.Context(), "tcp", "10.0.0.5:3389")
	requireDBTunnelCode(t, err, ipc.CodeDisconnected)

	// The database path never accepts a device jump.
	dbDial, err := newProductionDatabaseDialer(database, manager)(owner, "device/dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbDial(t.Context(), "tcp", "10.0.0.5:5432"); errors.Is(err, errDBTunnelTestDial) {
		t.Fatal("database tunnel dialed through a device")
	}
}

func TestSessionAssetDialerSSHAndDisconnectedErrors(t *testing.T) {
	database := newDBTunnelTestStore(t)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	via, err := database.AssetCreate(owner, store.AssetInput{Kind: session.KindSSH, Name: "jump", OptionsJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	idle := session.NewManager(session.Config{IdleTimeout: -1})
	t.Cleanup(func() { _ = idle.Close() })
	dial, err := newSessionAssetDialer(database, idle, true)(owner, via.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dial(t.Context(), "tcp", "10.0.0.5:3389")
	requireDBTunnelCode(t, err, ipc.CodeDisconnected)

	manager, _ := connectedViaManager(t, owner, via.ID, session.KindSSH)
	dial, _ = newSessionAssetDialer(database, manager, true)(owner, via.ID)
	if _, err := dial(t.Context(), "tcp", "10.0.0.5:5900"); !errors.Is(err, errDBTunnelTestDial) {
		t.Fatalf("ssh dial = %v", err)
	}
}

func TestTerminalConnectionOfRemoteDesktopAssetsFailsClearly(t *testing.T) {
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			t.Fatal("connector reached for a remote desktop asset")
			return nil, nil
		}),
		IdleTimeout: -1,
	})
	t.Cleanup(func() { _ = manager.Close() })
	owner := ipc.WithUserID(t.Context(), "owner-a")
	for _, kind := range []string{session.KindRDP, session.KindVNC} {
		_, err := manager.Connect(owner, session.Asset{ID: "a-" + kind, Kind: kind})
		if !errors.Is(err, session.ErrUnsupported) {
			t.Fatalf("%s: err = %v", kind, err)
		}
		if got := session.IPCError(err); got.Code != ipc.CodeUnsupported || got.Message == "" {
			t.Fatalf("%s: ipc error = %+v", kind, got)
		}
	}
}

func TestProbeRemoteDesktopAssetGoesThroughJumpSession(t *testing.T) {
	database := newDBTunnelTestStore(t)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	via, err := database.AssetCreate(owner, store.AssetInput{Kind: session.KindSSH, Name: "jump", OptionsJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	host := "10.9.9.9"
	row, err := database.AssetCreate(owner, store.AssetInput{Kind: session.KindRDP, Name: "win", Host: &host, OptionsJSON: `{"viaAssetId":"` + via.ID + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	manager, transport := connectedViaManager(t, owner, via.ID, session.KindSSH)
	service := &terminalCommandService{database: database, sessions: manager}
	target := service.resolveAssetProbeTarget(owner, row.ID)
	if target.viaAssetID != via.ID {
		t.Fatalf("viaAssetID = %q", target.viaAssetID)
	}
	// The fake transport refuses every dial, which proves the probe used the jump session and not a direct dial
	// to the unroutable address.
	err = service.probeReachability(owner, target, time.Second)
	if !errors.Is(err, errDBTunnelTestDial) {
		t.Fatalf("probe err = %v", err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.addresses) != 1 || transport.addresses[0] != "10.9.9.9:3389" {
		t.Fatalf("jump dial addresses = %v", transport.addresses)
	}
}
