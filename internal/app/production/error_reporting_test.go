package production

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"runtime"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/docker"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func newAppErrorTestProduction(t *testing.T, recorder ipc.Emitter, dockerService *docker.Service) *Production {
	t.Helper()
	config := ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{},
			Events:  recorder,
		},
		DataDir: t.TempDir(), Desktop: true,
	}
	if dockerService != nil {
		config.Docker = dockerService
	}
	production, err := NewProduction(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	return production
}

func closedTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestSessionConnectRefusedMessageIsActionable(t *testing.T) {
	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, closedTCPPort(t))

	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeSSH {
		t.Fatalf("connect = %+v, want ssh error", response)
	}
	if !strings.Contains(response.Error.Message, "拒绝连接") || !strings.Contains(response.Error.Message, "请确认主机在线") {
		t.Fatalf("message = %q, want actionable Chinese guidance", response.Error.Message)
	}
	encoded, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var detail sshConnectErrorDetail
	if err := json.Unmarshal(encoded, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Kind != "refused" || detail.Hint == "" {
		t.Fatalf("detail = %+v, want refused kind with technical hint", detail)
	}
}

func TestSessionReconnectUnknownSessionReturnsNotFound(t *testing.T) {
	production := newHostKeyTestProduction(t)
	response := dispatchHostKeyTest(t, production, "session_reconnect", `{"sessionId":"01M4CP5FYDZWFACTBB71ZYD009"}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("reconnect = %+v, want not_found", response)
	}
	if !strings.Contains(response.Error.Message, "会话不存在或已关闭") {
		t.Fatalf("message = %q, want actionable Chinese guidance", response.Error.Message)
	}
}

func TestAssetProbeBatchKeepsOriginalCauseInLog(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, closedTCPPort(t))

	response := dispatchHostKeyTest(t, production, "asset_probe_batch", `{"args":{"assetIds":["`+assetID+`"],"timeoutMs":2000}}`)
	var batch assetProbeBatchDTO
	requireStoreTestResponse(t, response, &batch)
	if len(batch.Results) != 1 {
		t.Fatalf("probe results = %+v", batch.Results)
	}
	result := batch.Results[0]
	if result.Reachable || result.Kind != "refused" {
		t.Fatalf("probe result = %+v, want refused", result)
	}
	if !strings.Contains(result.Error, "拒绝连接") {
		t.Fatalf("probe error = %q, want actionable Chinese message", result.Error)
	}
	if output := logs.String(); !strings.Contains(output, "connection refused") || !strings.Contains(output, assetID) {
		t.Fatalf("original cause missing from probe log: %q", output)
	}
}

func TestInitialCommandFailureEmitsAppErrorAndKeepsSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local shell initial command test relies on a POSIX shell")
	}
	recorder := &ipcEventRecorder{}
	production := newAppErrorTestProduction(t, recorder, nil)

	response := dispatchHostKeyTest(t, production, "asset_create", `{"args":{"kind":"local","name":"init-cmd-fail","options":{"initialCommand":"false"}}}`)
	var created assetDTO
	requireStoreTestResponse(t, response, &created)

	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+created.ID+`"}}`)
	var connected sessionInfoDTO
	requireStoreTestResponse(t, response, &connected)

	payload, ok := recorder.appError()
	if !ok {
		t.Fatal("initial command failure did not emit app://error")
	}
	if payload.Code != "session" || !strings.Contains(payload.Message, "初始命令执行失败") {
		t.Fatalf("app error payload = %+v", payload)
	}
}

func TestInitialCommandSuccessEmitsNoAppError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local shell initial command test relies on a POSIX shell")
	}
	recorder := &ipcEventRecorder{}
	production := newAppErrorTestProduction(t, recorder, nil)

	response := dispatchHostKeyTest(t, production, "asset_create", `{"args":{"kind":"local","name":"init-cmd-ok","options":{"initialCommand":"true"}}}`)
	var created assetDTO
	requireStoreTestResponse(t, response, &created)

	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+created.ID+`"}}`)
	var connected sessionInfoDTO
	requireStoreTestResponse(t, response, &connected)

	if payload, ok := recorder.appError(); ok {
		t.Fatalf("successful initial command emitted app error %+v", payload)
	}
}

func TestVaultListCredentialsReportsDecryptFailure(t *testing.T) {
	recorder := &ipcEventRecorder{}
	production := newAppErrorTestProduction(t, recorder, nil)
	initConnectorTestVault(t, production)

	response := dispatchHostKeyTest(t, production, "vault_set_credential",
		`{"args":{"name":"broken-key","kind":"private_key","secret":"key-content"}}`)
	var saved map[string]string
	requireStoreTestResponse(t, response, &saved)
	if _, err := production.Services.Store.CredentialPut(t.Context(), store.CredentialInput{
		ID: saved["id"], Name: "broken-key", Kind: vault.KindPrivateKey,
		Nonce: []byte("bad-nonce"), Blob: []byte("garbage-blob"), KEKHint: production.Services.Vault.KEKHint(),
	}); err != nil {
		t.Fatal(err)
	}

	response = dispatchHostKeyTest(t, production, "vault_list_credentials", `null`)
	var rows []credentialDTO
	requireStoreTestResponse(t, response, &rows)
	found := false
	for _, row := range rows {
		if row.ID != saved["id"] {
			continue
		}
		found = true
		if row.Source != nil || row.RefPath != nil || row.HasPassphrase {
			t.Fatalf("decrypt failure left phantom fields: %+v", row)
		}
	}
	if !found {
		t.Fatal("credential with failed decrypt missing from list")
	}

	payload, ok := recorder.appError()
	if !ok {
		t.Fatal("decrypt failure did not emit app://error")
	}
	if payload.Code != "vault" || !strings.Contains(payload.Message, "解密失败") {
		t.Fatalf("app error payload = %+v", payload)
	}
}

type failingCloseDockerProvider struct{ docker.StaticBackends }

func (failingCloseDockerProvider) CloseSession(string) error {
	return errors.New("docker daemon unreachable")
}

func TestSessionDisconnectReportsDockerCloseFailure(t *testing.T) {
	recorder := &ipcEventRecorder{}
	production := newAppErrorTestProduction(t, recorder, docker.NewService(failingCloseDockerProvider{}))

	response := dispatchHostKeyTest(t, production, "session_connect_local", `null`)
	var connected sessionInfoDTO
	requireStoreTestResponse(t, response, &connected)

	response = dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`)
	requireProductionNullHostKey(t, response)

	payload, ok := recorder.appError()
	if !ok {
		t.Fatal("docker close failure did not emit app://error")
	}
	if payload.Code != "docker" || !strings.Contains(payload.Message, "Docker") {
		t.Fatalf("app error payload = %+v", payload)
	}
}

func TestSessionReconnectReportsDockerCloseFailure(t *testing.T) {
	server := newHostKeyTestServer(t)
	recorder := &ipcEventRecorder{}
	production := newAppErrorTestProduction(t, recorder, docker.NewService(failingCloseDockerProvider{}))
	assetID := createHostKeyTestAsset(t, production, server.port())

	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	detail := requireHostKeyPending(t, response, false)
	acceptHostKeyTest(t, production, detail)
	connected := connectHostKeyTestAsset(t, production, assetID)

	response = dispatchHostKeyTest(t, production, "session_reconnect", `{"sessionId":"`+connected.ID+`"}`)
	var started bool
	requireStoreTestResponse(t, response, &started)
	if !started {
		t.Fatal("session_reconnect did not start")
	}

	payload, ok := recorder.appError()
	if !ok {
		t.Fatal("docker close failure did not emit app://error")
	}
	if payload.Code != "docker" || !strings.Contains(payload.Message, "Docker") {
		t.Fatalf("app error payload = %+v", payload)
	}
}
