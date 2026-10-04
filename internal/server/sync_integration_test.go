package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	syncservice "github.com/ProbiusOfficial/NexTerm/internal/sync"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

type realSyncFixture struct {
	db      *store.Store
	vault   *vault.Vault
	service *syncservice.Service
	token   string
}

func newRealSyncConfig(t *testing.T, syncOnly bool) (Config, *realSyncFixture) {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	credentialVault := vault.Load(ctx, db)
	if err := credentialVault.InitMaster(ctx, "sync-test-master"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(credentialVault.Lock)
	service := syncservice.New(db, credentialVault, syncservice.WithMetadata("server-test", false), syncservice.WithGatewayAuthKey("gateway-secret"))
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	token, err := service.SyncToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := ipc.NewDispatcher()
	if err := service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t, syncOnly)
	config.Dispatcher = dispatcher
	config.Tokens = service
	config.SyncRPC = service.PeerHandler()
	config.Vault = credentialVault
	return config, &realSyncFixture{db: db, vault: credentialVault, service: service, token: token}
}

func TestRealSyncServiceTokenGatewayHealthAndRPCBoundary(t *testing.T) {
	config, fixture := newRealSyncConfig(t, true)
	_, syncOnlyHTTP := newTestHTTP(t, config)
	ctx := context.Background()

	ensuredAgain, err := fixture.service.SyncToken(ctx)
	if err != nil || ensuredAgain != fixture.token {
		t.Fatalf("token ensure changed token: %q != %q, %v", ensuredAgain, fixture.token, err)
	}
	var stdout bytes.Buffer
	if err := RunTokenCommand(ctx, CommandToken, fixture.service, &stdout); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != fixture.token {
		t.Fatalf("CLI token = %q, want %q", stdout.String(), fixture.token)
	}
	if valid, err := fixture.service.VerifyToken(ctx, fixture.token); err != nil || !valid {
		t.Fatalf("valid token verification = %v, %v", valid, err)
	}

	status, body := postRPC(t, syncOnlyHTTP.Client(), syncOnlyHTTP.URL+"/sync/rpc", syncservice.CommandDigest, nil)
	if status != http.StatusUnauthorized || body.Error == nil || body.Error.Code != ipc.CodeForbidden {
		t.Fatalf("missing token = %d %+v", status, body)
	}
	status, body = postRPC(t, syncOnlyHTTP.Client(), syncOnlyHTTP.URL+"/sync/rpc", syncservice.CommandDigest, map[string]string{TokenHeader: fixture.token})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("valid token = %d %+v", status, body)
	}
	response, err := syncOnlyHTTP.Client().Get(syncOnlyHTTP.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var health Health
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !health.SyncOnly || health.Commands != len(SyncOnlyCommands()) || health.Commands != 3 || health.Vault == nil {
		t.Fatalf("sync-only health = %+v", health)
	}
	status, body = postRPC(t, syncOnlyHTTP.Client(), syncOnlyHTTP.URL+"/sync/rpc", syncservice.CommandToken, map[string]string{TokenHeader: fixture.token})
	if status != http.StatusOK || body.OK || body.Error == nil || body.Error.Code != ipc.CodeNotFound {
		t.Fatalf("full command through peer route = %d %+v", status, body)
	}
	response, err = syncOnlyHTTP.Client().Post(syncOnlyHTTP.URL+"/rpc", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("sync-only /rpc status = %d", response.StatusCode)
	}

	stdout.Reset()
	if err := RunTokenCommand(ctx, CommandRotateToken, fixture.service, &stdout); err != nil {
		t.Fatal(err)
	}
	rotated := strings.TrimSpace(stdout.String())
	if rotated == "" || rotated == fixture.token {
		t.Fatalf("rotated token = %q", rotated)
	}
	if valid, _ := fixture.service.VerifyToken(ctx, fixture.token); valid {
		t.Fatal("old token still verifies after rotation")
	}
	if valid, err := fixture.service.VerifyToken(ctx, rotated); err != nil || !valid {
		t.Fatalf("rotated token verification = %v, %v", valid, err)
	}
	status, _ = postRPC(t, syncOnlyHTTP.Client(), syncOnlyHTTP.URL+"/sync/rpc", syncservice.CommandDigest, map[string]string{TokenHeader: fixture.token})
	if status != http.StatusUnauthorized {
		t.Fatalf("old token HTTP status = %d", status)
	}
	status, body = postRPC(t, syncOnlyHTTP.Client(), syncOnlyHTTP.URL+"/sync/rpc", syncservice.CommandDigest, map[string]string{TokenHeader: rotated})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("rotated token HTTP = %d %+v", status, body)
	}
	status, body = postRPC(t, syncOnlyHTTP.Client(), syncOnlyHTTP.URL+"/sync/rpc", syncservice.CommandDigest, map[string]string{
		TokenHeader: "wrong", GatewayAuthHeader: "gateway-secret",
	})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("gateway key admission = %d %+v", status, body)
	}
	status, body = postRPC(t, syncOnlyHTTP.Client(), syncOnlyHTTP.URL+"/sync/rpc", syncservice.CommandDigest, map[string]string{
		TokenHeader: "wrong", "X-HC-User-ID": "forged",
	})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("forged platform identity = %d %+v", status, body)
	}

	fullConfig := config
	fullConfig.Options.SyncOnly = false
	fullConfig.Channels = unavailableChannels()
	_, fullHTTP := newTestHTTP(t, fullConfig)
	status, body = postRPC(t, fullHTTP.Client(), fullHTTP.URL+"/sync/rpc", syncservice.CommandToken, map[string]string{TokenHeader: rotated})
	if status != http.StatusOK || body.OK || body.Error == nil || body.Error.Code != ipc.CodeNotFound {
		t.Fatalf("full server peer boundary = %d %+v", status, body)
	}
	status, body = postRPC(t, fullHTTP.Client(), fullHTTP.URL+"/rpc", syncservice.CommandToken, nil)
	if status != http.StatusOK || !body.OK || string(body.Data) != string(mustJSON(t, rotated)) {
		t.Fatalf("full /rpc token response = %d %+v", status, body)
	}
}

func TestRealSyncNestedExportImportRoundTrip(t *testing.T) {
	config, fixture := newRealSyncConfig(t, true)
	_, httpServer := newTestHTTP(t, config)
	groupID, assetID, credentialID := ids.New(), ids.New(), ids.New()
	bundle := syncservice.Bundle{
		Protocol: syncservice.ProtocolVersion, Origin: "server-integration", ExportedAt: 1,
		Groups: []syncservice.GroupPayload{{ID: groupID, Name: "nested group", CreatedAt: 1, UpdatedAt: 1}},
		Assets: []syncservice.AssetPayload{{
			ID: assetID, GroupID: &groupID, CredID: &credentialID, Kind: "ssh", Name: "nested 中文",
			Host: syncIntegrationPtr("192.0.2.10"), AuthKind: syncIntegrationPtr("password"),
			OptionsJSON: `{}`, CreatedAt: 1, UpdatedAt: 1,
		}},
		Credentials: []syncservice.CredentialPayload{{ID: credentialID, Name: "nested credential", Kind: "password", Secret: "nested-secret-中文"}},
	}
	status, body := postNestedRPC(t, httpServer, syncservice.CommandImport, syncservice.ImportRequest{Bundle: bundle}, fixture.token)
	if status != http.StatusOK || !body.OK {
		t.Fatalf("nested import = %d %+v", status, body)
	}
	var report syncservice.ImportReport
	if err := json.Unmarshal(body.Data, &report); err != nil {
		t.Fatal(err)
	}
	if report.GroupsCreated != 1 || report.AssetsCreated != 1 || report.CredsCreated != 1 {
		t.Fatalf("nested import report = %+v", report)
	}
	asset, err := fixture.db.AssetGet(context.Background(), assetID)
	if err != nil || asset.Name != "nested 中文" {
		t.Fatalf("imported asset = %+v, %v", asset, err)
	}
	credential, err := fixture.db.CredentialGetRow(context.Background(), credentialID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := fixture.vault.DecryptCredentialString(context.Background(), credential)
	if err != nil || secret != "nested-secret-中文" {
		t.Fatalf("imported secret = %q, %v", secret, err)
	}

	status, body = postNestedRPC(t, httpServer, syncservice.CommandExport, syncservice.ExportRequest{AssetIDs: []string{assetID}, WithCredentials: true}, fixture.token)
	if status != http.StatusOK || !body.OK {
		t.Fatalf("nested export = %d %+v", status, body)
	}
	var exported syncservice.Bundle
	if err := json.Unmarshal(body.Data, &exported); err != nil {
		t.Fatal(err)
	}
	if exported.Protocol != syncservice.ProtocolVersion || len(exported.Groups) != 1 || len(exported.Assets) != 1 || len(exported.Credentials) != 1 || exported.Assets[0].ID != assetID || exported.Credentials[0].Secret != "nested-secret-中文" {
		t.Fatalf("nested export bundle = %+v", exported)
	}
}

func postNestedRPC(t *testing.T, httpServer *httptest.Server, command string, input any, token string) (int, ipc.Response) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"args": input})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(ipc.Request{Command: command, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/sync/rpc", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(TokenHeader, token)
	response, err := httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope ipc.Response
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, envelope
}

func syncIntegrationPtr[T any](value T) *T { return &value }

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
