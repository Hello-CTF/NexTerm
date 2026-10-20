package production

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var errDBTunnelTestDial = errors.New("test SSH dial reached")

type dbTunnelTestTransport struct {
	kind       string
	generation uint64
	alive      atomic.Bool
	dials      atomic.Int32
	mu         sync.Mutex
	addresses  []string
}

func newDBTunnelTestTransport(kind string, generation uint64) *dbTunnelTestTransport {
	transport := &dbTunnelTestTransport{kind: kind, generation: generation}
	transport.alive.Store(true)
	return transport
}

func (t *dbTunnelTestTransport) Kind() string       { return t.kind }
func (t *dbTunnelTestTransport) Generation() uint64 { return t.generation }
func (t *dbTunnelTestTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}
func (t *dbTunnelTestTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (t *dbTunnelTestTransport) IsAlive() bool                               { return t.alive.Load() }
func (t *dbTunnelTestTransport) Close() error {
	t.alive.Store(false)
	return nil
}
func (t *dbTunnelTestTransport) DialContext(ctx context.Context, _, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.dials.Add(1)
	t.mu.Lock()
	t.addresses = append(t.addresses, address)
	t.mu.Unlock()
	return nil, errDBTunnelTestDial
}

func newDBTunnelTestStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func requireDBTunnelCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != code {
		t.Fatalf("error = %#v, want code %s", err, code)
	}
}

func TestProductionPostgresDialerReconnectsWithoutRetainingConnectContext(t *testing.T) {
	database := newDBTunnelTestStore(t)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	via, err := database.AssetCreate(owner, store.AssetInput{Kind: session.KindSSH, Name: "jump", OptionsJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var transports []*dbTunnelTestTransport
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			mu.Lock()
			defer mu.Unlock()
			transport := newDBTunnelTestTransport(session.KindSSH, uint64(len(transports)+1))
			transports = append(transports, transport)
			return transport, nil
		}),
		ReconnectBackoff: []time.Duration{0},
		IdleTimeout:      -1,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(owner, session.Asset{ID: via.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}

	factoryCtx, cancelFactory := context.WithCancel(owner)
	dial, err := newProductionDatabaseDialer(database, manager)(factoryCtx, via.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelFactory()
	if _, err := dial(t.Context(), "tcp", "pg.internal:5432"); !errors.Is(err, errDBTunnelTestDial) {
		t.Fatalf("initial dial error = %v", err)
	}
	if err := manager.Reconnect(owner, connected.ID); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if _, err := dial(t.Context(), "tcp", "pg.internal:5432"); !errors.Is(err, errDBTunnelTestDial) {
		t.Fatalf("redial error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(transports) != 2 {
		t.Fatalf("transport count = %d, want 2", len(transports))
	}
	if transports[0].dials.Load() != 1 || transports[1].dials.Load() != 1 {
		t.Fatalf("transport dials = %d/%d", transports[0].dials.Load(), transports[1].dials.Load())
	}
	if got := connected.Info().Generation; got != 2 {
		t.Fatalf("session generation = %d, want 2", got)
	}
}

func TestProductionPostgresDialerRevalidatesAssetAndConnection(t *testing.T) {
	database := newDBTunnelTestStore(t)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	foreign := ipc.WithUserID(t.Context(), "owner-b")
	via, err := database.AssetCreate(owner, store.AssetInput{Kind: session.KindSSH, Name: "jump", OptionsJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	nonSSH, err := database.AssetCreate(owner, store.AssetInput{Kind: session.KindLocal, Name: "local", OptionsJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	transport := newDBTunnelTestTransport(session.KindSSH, 1)
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
		IdleTimeout: -1,
	})
	t.Cleanup(func() { _ = manager.Close() })
	factory := newProductionDatabaseDialer(database, manager)

	disconnected, err := factory(owner, via.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disconnected(t.Context(), "tcp", "pg.internal:5432"); err == nil {
		t.Fatal("dial succeeded without a connected SSH session")
	} else {
		requireDBTunnelCode(t, err, ipc.CodeDisconnected)
	}

	foreignDial, err := factory(foreign, via.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreignDial(t.Context(), "tcp", "pg.internal:5432"); err == nil {
		t.Fatal("foreign owner used the SSH asset")
	} else {
		requireDBTunnelCode(t, err, ipc.CodeNotFound)
	}

	wrongKind, err := factory(owner, nonSSH.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongKind(t.Context(), "tcp", "pg.internal:5432"); err == nil {
		t.Fatal("non-SSH asset was accepted")
	} else {
		requireDBTunnelCode(t, err, ipc.CodeBadParam)
	}

	connected, err := manager.Connect(owner, session.Asset{ID: via.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disconnected(t.Context(), "tcp", "pg.internal:5432"); !errors.Is(err, errDBTunnelTestDial) {
		t.Fatalf("connected dial error = %v", err)
	}
	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := disconnected(t.Context(), "tcp", "pg.internal:5432"); err == nil {
		t.Fatal("disconnected SSH session remained usable")
	} else {
		requireDBTunnelCode(t, err, ipc.CodeDisconnected)
	}
	if err := manager.Reconnect(owner, connected.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.AssetDelete(owner, via.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := disconnected(t.Context(), "tcp", "pg.internal:5432"); err == nil {
		t.Fatal("deleted SSH asset remained usable")
	} else {
		requireDBTunnelCode(t, err, ipc.CodeNotFound)
	}
}

func TestProductionDatabaseServiceSurfacesDisconnectedTunnel(t *testing.T) {
	database := newDBTunnelTestStore(t)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	via, err := database.AssetCreate(owner, store.AssetInput{Kind: session.KindSSH, Name: "jump", OptionsJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	host := "pg.tunnel.invalid"
	target, err := database.AssetCreate(owner, store.AssetInput{
		Kind: "postgres", Name: "database", Host: &host,
		OptionsJSON: fmt.Sprintf(`{"database":"app","viaAssetId":%q}`, via.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(session.Config{IdleTimeout: -1})
	t.Cleanup(func() { _ = manager.Close() })
	service := newProductionDatabaseService(database, nil, manager)
	t.Cleanup(func() { _ = service.Close() })
	_, err = service.Connect(owner, db.ConnectArgs{AssetID: target.ID})
	requireDBTunnelCode(t, err, ipc.CodeDisconnected)
}
