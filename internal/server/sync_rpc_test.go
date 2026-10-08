package server

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

// newSyncRPCFixture 装配真实同步服务对端命令面(PeerDispatcher)与账号体系的测试服务器。
func newSyncRPCFixture(t *testing.T, syncOnly bool) (*accountFixture, *ipc.Dispatcher) {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := account.New(database.DB())
	_, _, syncService := newRealSyncService(t)
	peer, err := syncService.PeerDispatcher()
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig(t, syncOnly)
	config.Accounts = accounts
	config.PeerDispatcher = peer
	server, httpServer := newTestHTTP(t, config)
	return &accountFixture{
		server: server, http: httpServer, accounts: accounts, db: database.DB(),
		client: &http.Client{},
	}, peer
}

func TestSyncOnlyPeerRPCHHealthReportsPeerCommandCount(t *testing.T) {
	fixture, peer := newSyncRPCFixture(t, true)

	if got, want := peer.Commands(), []string{"sync_digest", "sync_export", "sync_import"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("peer command surface = %v, want %v", got, want)
	}
	call := fixture.call(t, http.MethodGet, "/healthz", nil, nil, "", nil)
	if call.status != http.StatusOK || call.body["ok"] != true || call.body["service"] != "nexterm-server" {
		t.Fatalf("sync-only health status=%d body=%v", call.status, call.body)
	}
	if call.body["syncOnly"] != true || call.body["commands"] != float64(3) {
		t.Fatalf("sync-only health must report the real peer command count: %v", call.body)
	}
}

func TestSyncOnlyPeerRPCSessionAndCSRFBoundaries(t *testing.T) {
	fixture, _ := newSyncRPCFixture(t, true)
	session, _ := fixture.initSuperadmin(t, "alice", "alice-pw-123")

	digest := map[string]any{"cmd": "sync_digest", "args": map[string]any{}}
	if call := fixture.call(t, http.MethodPost, "/sync/rpc", digest, nil, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("anonymous peer rpc status=%d body=%v", call.status, call.body)
	}
	stale := &accountTestSession{cookie: &http.Cookie{Name: sessionCookieName, Value: "stale"}}
	if call := fixture.call(t, http.MethodPost, "/sync/rpc", digest, stale, session.csrf, nil); call.status != http.StatusUnauthorized {
		t.Fatalf("stale session peer rpc status=%d body=%v", call.status, call.body)
	}
	call := fixture.call(t, http.MethodPost, "/sync/rpc", digest, session, "", nil)
	if call.status != http.StatusForbidden {
		t.Fatalf("peer rpc without CSRF status=%d body=%v", call.status, call.body)
	}
	if message, _ := call.body["error"].(map[string]any)["message"].(string); !strings.Contains(message, "CSRF") {
		t.Fatalf("CSRF rejection message=%q, want CSRF keyword", message)
	}
	if call := fixture.call(t, http.MethodPost, "/sync/rpc", digest, session, "garbage", nil); call.status != http.StatusForbidden {
		t.Fatalf("peer rpc with bad CSRF status=%d body=%v", call.status, call.body)
	}

	call = fixture.call(t, http.MethodPost, "/sync/rpc", digest, session, session.csrf, nil)
	if call.status != http.StatusOK || call.body["ok"] != true {
		t.Fatalf("peer digest with session status=%d body=%v", call.status, call.body)
	}
	data, _ := call.body["data"].(map[string]any)
	if data["origin"] != "server" || data["assets"] == nil {
		t.Fatalf("peer digest data=%v", data)
	}
}

func TestSyncOnlyPeerRPCExactCommandSurface(t *testing.T) {
	fixture, _ := newSyncRPCFixture(t, true)
	session, _ := fixture.initSuperadmin(t, "alice", "alice-pw-123")

	export := fixture.call(t, http.MethodPost, "/sync/rpc", map[string]any{
		"cmd": "sync_export", "args": map[string]any{"args": map[string]any{"assetIds": []string{}, "withCreds": false}},
	}, session, session.csrf, nil)
	if export.status != http.StatusOK || export.body["ok"] != true {
		t.Fatalf("peer export status=%d body=%v", export.status, export.body)
	}
	bundle, _ := export.body["data"].(map[string]any)
	if bundle["protocol"] != float64(1) || bundle["origin"] != "server" {
		t.Fatalf("peer export bundle=%v", bundle)
	}
	imported := fixture.call(t, http.MethodPost, "/sync/rpc", map[string]any{
		"cmd": "sync_import", "args": map[string]any{"args": map[string]any{"bundle": bundle, "force": false}},
	}, session, session.csrf, nil)
	if imported.status != http.StatusOK || imported.body["ok"] != true {
		t.Fatalf("peer import status=%d body=%v", imported.status, imported.body)
	}

	for _, command := range []string{"app_info", "sync_status", "sync_bundle_read"} {
		call := fixture.call(t, http.MethodPost, "/sync/rpc", map[string]any{
			"cmd": command, "args": map[string]any{},
		}, session, session.csrf, nil)
		if call.status != http.StatusOK || call.body["ok"] != false {
			t.Fatalf("peer rpc %s status=%d body=%v, want not_found failure", command, call.status, call.body)
		}
		if code, _ := call.body["error"].(map[string]any)["code"].(string); code != string(ipc.CodeNotFound) {
			t.Fatalf("peer rpc %s error=%v, want not_found", command, call.body["error"])
		}
	}
}

func TestSyncRPCNotMountedInFullMode(t *testing.T) {
	fixture, peer := newSyncRPCFixture(t, false)
	session, _ := fixture.initSuperadmin(t, "alice", "alice-pw-123")

	call := fixture.call(t, http.MethodPost, "/sync/rpc", map[string]any{
		"cmd": "sync_digest", "args": map[string]any{},
	}, session, session.csrf, nil)
	if call.status != http.StatusNotFound {
		t.Fatalf("full-mode /sync/rpc status=%d, want 404", call.status)
	}
	health := fixture.call(t, http.MethodGet, "/healthz", nil, nil, "", nil)
	if health.status != http.StatusOK || health.body["syncOnly"] != false {
		t.Fatalf("full health status=%d body=%v", health.status, health.body)
	}
	commands, _ := health.body["commands"].(float64)
	if commands != float64(fixture.server.dispatcher.Len()) || int(commands) == peer.Len() {
		t.Fatalf("full health must keep reporting the full dispatcher count, not the peer count: %v", health.body["commands"])
	}
}
