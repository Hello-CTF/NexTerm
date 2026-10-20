package production

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/server"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const productionHostKeyGatewayKey = "host-key-gateway-test"

type productionHostKeyPlatformFixture struct {
	application *Production
	http        *httptest.Server
	cookie      *http.Cookie
	csrf        string
	ownerID     string
}

func TestProductionHostKeyPlatformGatewayCookieSSH(t *testing.T) {
	sshServer := newHostKeyTestServer(t)
	fixture := newProductionHostKeyPlatformFixture(t)
	_, response := fixture.rpc(t, "asset_create", map[string]any{"args": map[string]any{
		"kind": "ssh", "name": "platform-host-key", "host": "127.0.0.1", "port": sshServer.port(),
		"username": "test", "authKind": "password", "options": map[string]any{"password": "host-key-test"},
	}}, true)
	var asset assetDTO
	requireStoreTestResponse(t, response, &asset)
	connectArgs := map[string]any{"args": map[string]any{"assetId": asset.ID}}
	status, response := fixture.rpc(t, "session_connect", connectArgs, true)
	if status != http.StatusOK {
		t.Fatalf("platform session connect status = %d, response=%+v", status, response)
	}
	if response.Error != nil && response.Error.Code != ipc.CodeHostKeyPending {
		t.Fatalf("platform initial SSH code=%s message=%q detail=%+v", response.Error.Code, response.Error.Message, response.Error.Detail)
	}
	pending := requireHostKeyPending(t, response, false)
	acceptArgs := map[string]any{
		"host": pending.Host, "port": pending.Port, "keyType": pending.KeyType, "fingerprint": pending.Fingerprint,
	}
	status, response = fixture.rpcWithoutGateway(t, "session_connect", connectArgs)
	if status != http.StatusUnauthorized || response.OK {
		t.Fatalf("cookie-only platform SSH = %d %+v, want gateway rejection", status, response)
	}

	for _, call := range []struct {
		command string
		args    any
	}{
		{command: "known_host_list", args: nil},
		{command: "known_host_accept", args: acceptArgs},
		{command: "session_connect", args: connectArgs},
	} {
		status, response = fixture.rpc(t, call.command, call.args, false)
		if call.command == "session_connect" {
			if response.OK || response.Error == nil {
				t.Fatalf("gateway-only %s = %d %+v, want owner rejection", call.command, status, response)
			}
		} else if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
			t.Fatalf("gateway-only %s = %d %+v, want forbidden", call.command, status, response)
		}
	}
	status, response = fixture.rpc(t, "known_host_list", nil, true)
	if status != http.StatusOK {
		t.Fatalf("platform list status = %d", status)
	}
	var rows []store.KnownHostRow
	requireStoreTestResponse(t, response, &rows)
	if len(rows) != 0 {
		t.Fatalf("gateway-only accept persisted ledger rows: %+v", rows)
	}

	status, response = fixture.rpc(t, "known_host_accept", acceptArgs, true)
	if status != http.StatusOK {
		t.Fatalf("platform accept status = %d response=%+v", status, response)
	}
	requireProductionNullHostKey(t, response)
	status, response = fixture.rpc(t, "session_connect", connectArgs, true)
	if status != http.StatusOK {
		t.Fatalf("platform accepted connect status = %d response=%+v", status, response)
	}
	var connected sessionInfoDTO
	requireStoreTestResponse(t, response, &connected)
	if connected.ID == "" {
		t.Fatal("platform gateway plus cookie did not complete the SSH connection")
	}
	status, response = fixture.rpc(t, "known_host_list", nil, true)
	if status != http.StatusOK {
		t.Fatalf("platform accepted list status = %d", status)
	}
	rows = nil
	requireStoreTestResponse(t, response, &rows)
	if len(rows) != 1 || rows[0].OwnerID != fixture.ownerID || rows[0].Fingerprint != pending.Fingerprint {
		t.Fatalf("platform owner ledger = %+v, want owner %q and accepted fingerprint", rows, fixture.ownerID)
	}
	status, response = fixture.rpc(t, "session_disconnect", map[string]any{"sessionId": connected.ID}, true)
	if status != http.StatusOK {
		t.Fatalf("platform disconnect status = %d response=%+v", status, response)
	}
	requireProductionNullHostKey(t, response)
}

func newProductionHostKeyPlatformFixture(t *testing.T) *productionHostKeyPlatformFixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dataDir := t.TempDir()
	application, err := NewProduction(context.Background(), ProductionConfig{
		Config: Config{Logger: logger, Streams: ipc.StreamFactoryFuncs{}}, DataDir: dataDir, Desktop: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	accounts := account.New(application.Services.Store.GORM())
	transport, err := server.New(server.Config{
		Options:        server.Options{Listen: "0.0.0.0:8080", DataDir: dataDir, Auth: server.AuthPlatform},
		Dispatcher:     application.Dispatcher,
		Environment:    application.Environment(""),
		Accounts:       accounts,
		SyncObjects:    application.SyncObjectHandler(),
		GatewayAuthKey: productionHostKeyGatewayKey,
		Channels:       server.NewHubAdapter(application.Services.Sessions.Hub()),
		Logger:         logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	httpServer := httptest.NewServer(transport.Handler())
	t.Cleanup(httpServer.Close)
	fixture := &productionHostKeyPlatformFixture{application: application, http: httpServer}
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/auth/platform-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(server.GatewayAuthHeader, productionHostKeyGatewayKey)
	response, err := httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("platform session status = %d", response.StatusCode)
	}
	var body struct {
		CSRFToken string `json:"csrf_token"`
		User      struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.CSRFToken == "" || body.User.ID == "" {
		t.Fatalf("platform session omitted owner or CSRF: %+v", body)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == "nexterm_session" {
			fixture.cookie = cookie
		}
	}
	if fixture.cookie == nil {
		t.Fatal("platform session did not issue a cookie")
	}
	fixture.csrf = body.CSRFToken
	fixture.ownerID = body.User.ID
	return fixture
}

func (f *productionHostKeyPlatformFixture) rpc(t *testing.T, command string, args any, withSession bool) (int, ipc.Response) {
	t.Helper()
	return f.rpcRequest(t, command, args, true, withSession)
}

func (f *productionHostKeyPlatformFixture) rpcWithoutGateway(t *testing.T, command string, args any) (int, ipc.Response) {
	t.Helper()
	return f.rpcRequest(t, command, args, false, true)
}

func (f *productionHostKeyPlatformFixture) rpcRequest(t *testing.T, command string, args any, withGateway, withSession bool) (int, ipc.Response) {
	t.Helper()
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	requestJSON, err := json.Marshal(ipc.Request{Command: command, Args: argsJSON, ClientID: "platform-host-key-test"})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, f.http.URL+"/rpc", bytes.NewReader(requestJSON))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if withGateway {
		request.Header.Set(server.GatewayAuthHeader, productionHostKeyGatewayKey)
	}
	if withSession {
		request.AddCookie(f.cookie)
		request.Header.Set("X-NexTerm-CSRF", f.csrf)
	}
	response, err := f.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body ipc.Response
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}
