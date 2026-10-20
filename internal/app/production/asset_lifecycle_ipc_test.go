package production

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type assetLifecycleTransport struct {
	kind       string
	generation uint64
	alive      atomic.Bool
}

func (t *assetLifecycleTransport) Kind() string       { return t.kind }
func (t *assetLifecycleTransport) Generation() uint64 { return t.generation }
func (t *assetLifecycleTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}
func (t *assetLifecycleTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (t *assetLifecycleTransport) IsAlive() bool                               { return t.alive.Load() }
func (t *assetLifecycleTransport) Close() error {
	t.alive.Store(false)
	return nil
}

type assetLifecycleConnector struct {
	mu    sync.Mutex
	ports []int
}

func (c *assetLifecycleConnector) Connect(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
	port := 0
	if options, ok := asset.Options.(map[string]any); ok {
		switch value := options["port"].(type) {
		case int32:
			port = int(value)
		case int:
			port = value
		case float64:
			port = int(value)
		}
	}
	c.mu.Lock()
	c.ports = append(c.ports, port)
	c.mu.Unlock()
	transport := &assetLifecycleTransport{kind: asset.Kind, generation: generation}
	transport.alive.Store(true)
	return transport, nil
}

func (c *assetLifecycleConnector) snapshotPorts() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.ports...)
}

func newAssetLifecycleFixture(t *testing.T) (*ipc.Dispatcher, *store.Store, *session.Manager, *assetLifecycleConnector) {
	t.Helper()
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	connector := &assetLifecycleConnector{}
	manager := session.NewManager(session.Config{
		Connector: connector, IdleTimeout: -1, ReconnectMax: 1, ReconnectBackoff: []time.Duration{time.Millisecond},
	})
	t.Cleanup(func() { _ = manager.Close() })
	service := newTerminalCommandService(database, manager, nil, nil, nil, nil, nil, nil, true)
	dispatcher := ipc.NewDispatcher()
	if err := service.registerSession(dispatcher); err != nil {
		t.Fatal(err)
	}
	if err := registerStoreCommands(dispatcher, database, nil, "", true); err != nil {
		t.Fatal(err)
	}
	return dispatcher, database, manager, connector
}

func createAssetLifecycleAsset(t *testing.T, ctx context.Context, database *store.Store, name string, port int32) store.AssetRow {
	t.Helper()
	host := "127.0.0.1"
	asset, err := database.AssetCreate(ctx, store.AssetInput{
		Kind: session.KindSSH, Name: name, Host: &host, Port: &port, OptionsJSON: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func dispatchAssetLifecycle(ctx context.Context, dispatcher *ipc.Dispatcher, command, args string) ipc.Response {
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func connectAssetLifecycle(t *testing.T, ctx context.Context, dispatcher *ipc.Dispatcher, assetID string) sessionInfoDTO {
	t.Helper()
	response := dispatchAssetLifecycle(ctx, dispatcher, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	var connected sessionInfoDTO
	requireStoreTestResponse(t, response, &connected)
	return connected
}

func listAssetLifecycleSessions(t *testing.T, ctx context.Context, dispatcher *ipc.Dispatcher) []sessionInfoDTO {
	t.Helper()
	response := dispatchAssetLifecycle(ctx, dispatcher, "session_list", `null`)
	var infos []sessionInfoDTO
	requireStoreTestResponse(t, response, &infos)
	return infos
}

func waitAssetLifecycleReconnect(t *testing.T, manager *session.Manager, connector *assetLifecycleConnector, sessionID string, calls int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(connector.snapshotPorts()) >= calls {
			connected, err := manager.Session(sessionID)
			if err == nil && connected.Info().Status == session.StatusConnected {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("reconnect did not finish: ports=%v info=%+v", connector.snapshotPorts(), manager.ListSessions())
}

func TestAssetLifecycleSoftDeletedAssetsCannotConnectOrProbe(t *testing.T) {
	ctx := t.Context()
	dispatcher, database, _, _ := newAssetLifecycleFixture(t)
	asset := createAssetLifecycleAsset(t, ctx, database, "deleted-asset", 22)
	connected := connectAssetLifecycle(t, ctx, dispatcher, asset.ID)
	if err := database.AssetDelete(ctx, asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AssetGet(ctx, asset.ID); err != nil {
		t.Fatalf("AssetGet must retain the tombstone for sync and repair: %v", err)
	}

	for _, command := range []string{"session_connect", "session_probe_host_key"} {
		response := dispatchAssetLifecycle(ctx, dispatcher, command, `{"args":{"assetId":"`+asset.ID+`"}}`)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
			t.Fatalf("%s deleted asset = %+v, want not_found", command, response)
		}
	}
	response := dispatchAssetLifecycle(ctx, dispatcher, "asset_probe_batch", `{"args":{"assetIds":["`+asset.ID+`"]}}`)
	var batch assetProbeBatchDTO
	requireStoreTestResponse(t, response, &batch)
	if len(batch.Results) != 1 || batch.Results[0].Kind != "not_found" || batch.Results[0].Reachable {
		t.Fatalf("deleted batch probe = %+v, want not_found", batch.Results)
	}
	response = dispatchAssetLifecycle(ctx, dispatcher, "session_reconnect", `{"sessionId":"`+connected.ID+`"}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("deleted asset manual reconnect = %+v, want not_found", response)
	}
}

func TestAssetLifecycleManualReconnectUsesNewConfigAutomaticReconnectKeepsSnapshot(t *testing.T) {
	ctx := t.Context()
	dispatcher, database, manager, connector := newAssetLifecycleFixture(t)
	asset := createAssetLifecycleAsset(t, ctx, database, "reconnect-asset", 2201)
	connected := connectAssetLifecycle(t, ctx, dispatcher, asset.ID)

	response := dispatchAssetLifecycle(ctx, dispatcher, "asset_update", `{"args":{"id":"`+asset.ID+`","port":2202}}`)
	var updated assetDTO
	requireStoreTestResponse(t, response, &updated)
	infos := listAssetLifecycleSessions(t, ctx, dispatcher)
	if len(infos) != 1 || !infos[0].StaleConfig {
		t.Fatalf("interactive port edit did not mark stale config: %+v", infos)
	}

	response = dispatchAssetLifecycle(ctx, dispatcher, "session_reconnect", `{"sessionId":"`+connected.ID+`"}`)
	var started bool
	requireStoreTestResponse(t, response, &started)
	if !started {
		t.Fatal("manual reconnect did not start")
	}
	waitAssetLifecycleReconnect(t, manager, connector, connected.ID, 2)
	if got := connector.snapshotPorts(); len(got) != 2 || got[0] != 2201 || got[1] != 2202 {
		t.Fatalf("manual reconnect ports = %v, want [2201 2202]", got)
	}
	current, err := manager.Session(connected.ID)
	if err != nil || current.Info().StaleConfig {
		t.Fatalf("manual reconnect did not clear stale config: %+v, %v", current, err)
	}

	row, err := database.AssetGet(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	port := int32(2203)
	row.Port = &port
	row.UpdatedAt++
	if _, err := database.AssetUpsert(ctx, row); err != nil {
		t.Fatal(err)
	}
	infos = listAssetLifecycleSessions(t, ctx, dispatcher)
	if len(infos) != 1 || !infos[0].StaleConfig {
		t.Fatalf("sync upsert did not mark stale config: %+v", infos)
	}
	if err := manager.StartReconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	waitAssetLifecycleReconnect(t, manager, connector, connected.ID, 3)
	if got := connector.snapshotPorts(); len(got) != 3 || got[2] != 2202 {
		t.Fatalf("automatic reconnect used a new snapshot: ports=%v, want third port 2202", got)
	}
	if info := current.Info(); !info.StaleConfig {
		t.Fatalf("automatic reconnect cleared stale config: %+v", info)
	}
}

func TestAssetLifecycleStaleConfigIsOwnerScoped(t *testing.T) {
	ctxA := ipc.WithUserID(t.Context(), "owner-a")
	ctxB := ipc.WithUserID(t.Context(), "owner-b")
	dispatcher, database, manager, _ := newAssetLifecycleFixture(t)
	assetA := createAssetLifecycleAsset(t, ctxA, database, "owner-a-asset", 2201)
	assetB := createAssetLifecycleAsset(t, ctxB, database, "owner-b-asset", 2202)
	connectedA := connectAssetLifecycle(t, ctxA, dispatcher, assetA.ID)
	connectedB := connectAssetLifecycle(t, ctxB, dispatcher, assetB.ID)
	if err := manager.MarkSessionConfigStaleFor(ctxA, connectedB.ID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("cross-owner stale mark error = %v, want forbidden", err)
	}
	if _, err := database.AssetUpdate(ctxB, assetB.ID, store.AssetPatch{Port: store.Value(int32(2203))}); err != nil {
		t.Fatal(err)
	}
	infosA := listAssetLifecycleSessions(t, ctxA, dispatcher)
	if len(infosA) != 1 || infosA[0].ID != connectedA.ID || infosA[0].StaleConfig {
		t.Fatalf("owner A session was affected by owner B edit: %+v", infosA)
	}
	infosB := listAssetLifecycleSessions(t, ctxB, dispatcher)
	if len(infosB) != 1 || infosB[0].ID != connectedB.ID || !infosB[0].StaleConfig {
		t.Fatalf("owner B session was not marked: %+v", infosB)
	}
}
