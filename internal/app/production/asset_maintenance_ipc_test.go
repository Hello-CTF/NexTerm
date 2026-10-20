package production

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	gossh "golang.org/x/crypto/ssh"
)

func maintenanceTestPublicKey(t *testing.T, comment string) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := gossh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(gossh.MarshalAuthorizedKey(key))) + " " + comment
}

func TestPublicKeyDeployCommandIdempotentBackupAndEscaping(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	home := t.TempDir()
	remoteDirectory := "odd'$(touch injected-path-canary)"
	actualDirectory := filepath.Join(home, remoteDirectory)
	if err := os.MkdirAll(actualDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	authorizedKeys := filepath.Join(actualDirectory, "authorized keys")
	initial := "existing-entry-without-newline"
	if err := os.WriteFile(authorizedKeys, []byte(initial), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(authorizedKeys, 0o640); err != nil {
		t.Fatal(err)
	}
	publicKey := maintenanceTestPublicKey(t, "operator'$(touch injected-key-canary)")
	command, err := buildPublicKeyDeployCommand(publicKey, "~/"+remoteDirectory+"/authorized keys")
	if err != nil {
		t.Fatal(err)
	}
	run := func() string {
		t.Helper()
		cmd := exec.Command("sh", "-c", command)
		cmd.Dir = home
		env := make([]string, 0, len(os.Environ())+1)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "HOME=") {
				env = append(env, value)
			}
		}
		cmd.Env = append(env, "HOME="+home)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("deploy command failed: %v\n%s\ncommand: %s", err, output, command)
		}
		return string(output)
	}
	firstOutput := run()
	if !strings.Contains(firstOutput, publicKeyDeployAddedMarker) || !strings.Contains(firstOutput, publicKeyDeployBackupMarker) {
		t.Fatalf("first deploy output = %q", firstOutput)
	}
	wantContent := initial + "\n" + publicKey + "\n"
	content, err := os.ReadFile(authorizedKeys)
	if err != nil || string(content) != wantContent {
		t.Fatalf("authorized_keys = %q, want %q; err=%v", content, wantContent, err)
	}
	modeAfterFirst, err := os.Stat(authorizedKeys)
	if err != nil {
		t.Fatal(err)
	}
	if modeAfterFirst.Mode().Perm() != 0o640 {
		t.Fatalf("authorized_keys mode after first deploy = %v, want 0640", modeAfterFirst.Mode())
	}
	backups, err := filepath.Glob(filepath.Join(actualDirectory, "authorized keys.nexterm-backup-*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %+v, err=%v", backups, err)
	}
	backupContent, err := os.ReadFile(backups[0])
	if err != nil || string(backupContent) != initial {
		t.Fatalf("backup content = %q, err=%v", backupContent, err)
	}
	backupInfo, err := os.Stat(backups[0])
	if err != nil || backupInfo.Mode().Perm()&0o222 != 0 {
		t.Fatalf("backup is not read-only: mode=%v, err=%v", backupInfo.Mode(), err)
	}

	secondOutput := run()
	if !strings.Contains(secondOutput, publicKeyDeployPresentMarker) {
		t.Fatalf("idempotent deploy output = %q", secondOutput)
	}
	content, err = os.ReadFile(authorizedKeys)
	if err != nil || string(content) != wantContent {
		t.Fatalf("authorized_keys after second deploy = %q, err=%v", content, err)
	}
	modeAfterSecond, err := os.Stat(authorizedKeys)
	if err != nil {
		t.Fatal(err)
	}
	if modeAfterSecond.Mode().Perm() != 0o640 {
		t.Fatalf("authorized_keys mode after second deploy = %v, want 0640", modeAfterSecond.Mode())
	}
	secondBackups, err := filepath.Glob(filepath.Join(actualDirectory, "authorized keys.nexterm-backup-*"))
	if err != nil || len(secondBackups) != 1 || secondBackups[0] != backups[0] {
		t.Fatalf("second deploy changed backups: %+v, err=%v", secondBackups, err)
	}
	for _, canary := range []string{"injected-key-canary", "injected-path-canary"} {
		if _, err := os.Stat(filepath.Join(home, canary)); !os.IsNotExist(err) {
			t.Fatalf("shell injection canary %s exists or stat failed: %v", canary, err)
		}
	}
}

func TestPublicKeyDeployCommandValidation(t *testing.T) {
	valid := maintenanceTestPublicKey(t, "valid")
	for name, test := range map[string]struct {
		key  string
		path string
	}{
		"invalid key":       {key: "not-a-public-key"},
		"multiline key":     {key: valid + "\nrm -rf /"},
		"relative path":     {key: valid, path: "../authorized_keys"},
		"path with newline": {key: valid, path: "/tmp/authorized_keys\nid"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := buildPublicKeyDeployCommand(test.key, test.path); err == nil {
				t.Fatal("invalid deploy input was accepted")
			}
		})
	}
}

func TestDeployPublicKeyIPCRequiresProductionConfirmation(t *testing.T) {
	var execCalls atomic.Int32
	commands := make(chan string, 1)
	transport := newExecBatchTestTransport(func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
		execCalls.Add(1)
		commands <- command
		code := 0
		return base.ExecResult{ExitCode: &code, Stdout: publicKeyDeployAddedMarker + "\n"}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	owner := ipc.WithUserID(t.Context(), "deploy-owner")
	connected, err := manager.ConnectFor(owner, session.Asset{ID: "asset-production", Name: "Production", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	auditor := &execBatchProductionAuditor{}
	batches := newExecBatchService(manager, execBatchTestAssets(map[string]store.AssetRow{
		"asset-production": {ID: "asset-production", Name: "Production", Tags: "env:prod"},
	}), auditor)
	dispatcher := ipc.NewDispatcher()
	if err := registerAssetMaintenanceCommands(dispatcher, nil, batches, nil); err != nil {
		t.Fatal(err)
	}
	request := DeployPublicKeyRequest{
		BatchID: "deploy-production", SessionIDs: []string{connected.ID}, PublicKey: maintenanceTestPublicKey(t, "deploy"),
	}
	response := dispatchExecBatchTest(dispatcher, owner, "exec_batch_deploy_public_key", request)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm || execCalls.Load() != 0 {
		t.Fatalf("unconfirmed deploy response = %+v, calls=%d", response, execCalls.Load())
	}
	encodedError, err := json.Marshal(response.Error)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedError), request.PublicKey) {
		t.Fatalf("confirmation response contains the deploy payload: %s", encodedError)
	}
	encodedDetail, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var detail execbatch.ConfirmationRequiredDetail
	if err := json.Unmarshal(encodedDetail, &detail); err != nil {
		t.Fatal(err)
	}
	request.ProductionConfirmation = &execbatch.ProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
	}
	confirmed := dispatchExecBatchTest(dispatcher, owner, "exec_batch_deploy_public_key", request)
	batch := decodeExecBatchTest[execbatch.Batch](t, confirmed)
	if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusSuccess || execCalls.Load() != 1 {
		t.Fatalf("confirmed deploy = %+v, calls=%d", batch, execCalls.Load())
	}
	select {
	case command := <-commands:
		want, err := buildPublicKeyDeployCommand(request.PublicKey, "")
		if err != nil || command != want {
			t.Fatalf("executed deploy command mismatch: err=%v", err)
		}
	default:
		t.Fatal("deploy command was not captured")
	}
}

func TestAssetBulkEditIPCPerTargetResultsWithoutSecret(t *testing.T) {
	production := newHostKeyTestProduction(t)
	ctx := context.Background()
	const secret = "bulk-ipc-secret-never-return"
	credentialID, err := production.Services.Store.CredentialPut(ctx, store.CredentialInput{
		Name: "bulk-ipc", Kind: "private_key", Nonce: []byte{1}, Blob: []byte(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := production.Services.Store.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "first", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := production.Services.Store.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "second", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	tags := "maintained"
	response := dispatchExecBatchTest(production.Dispatcher, ctx, "exec_batch_asset_edit", AssetBulkEditIPCRequest{
		Targets: []store.AssetBulkEditTarget{
			{AssetID: first.ID, ExpectedUpdatedAt: first.UpdatedAt},
			{AssetID: second.ID, ExpectedUpdatedAt: second.UpdatedAt + 1},
		},
		Tags: &tags, CredentialID: &credentialID,
	})
	result := decodeExecBatchTest[AssetBulkEditIPCResponse](t, response)
	if len(result.Results) != 2 || result.Results[0].Error != "" || result.Results[0].UpdatedAt <= first.UpdatedAt {
		t.Fatalf("bulk IPC results = %+v", result)
	}
	if !result.Results[1].Conflict || result.Results[1].Error == "" {
		t.Fatalf("bulk IPC conflict = %+v", result.Results[1])
	}
	if strings.Contains(string(response.Data), secret) {
		t.Fatalf("bulk IPC response contains credential plaintext: %s", response.Data)
	}
	updated, err := production.Services.Store.AssetGet(ctx, first.ID)
	if err != nil || updated.CredID == nil || *updated.CredID != credentialID || updated.Tags != tags {
		t.Fatalf("bulk IPC updated asset = %+v, err=%v", updated, err)
	}
}

func TestTestLoginIPCResultOwnershipWithoutReconnect(t *testing.T) {
	production := newHostKeyTestProduction(t)
	assetID := createProbeTestAsset(t, production, `"host":"127.0.0.1","port":`+itoa(closedProbePort(t))+`,"username":"test","authKind":"password","options":{"password":"probe-test-secret"}`)
	payload, err := json.Marshal(map[string]any{"assetIds": []string{assetID, "missing", assetID}, "timeoutMs": 500})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatchHostKeyTest(t, production, "exec_batch_test_login", `{"args":`+string(payload)+`}`)
	var batch assetProbeBatchDTO
	requireStoreTestResponse(t, response, &batch)
	if len(batch.Results) != 2 || batch.Results[0].AssetID != assetID || batch.Results[1].AssetID != "missing" {
		t.Fatalf("test login result ownership = %+v", batch)
	}
	if batch.Results[0].Reachable || batch.Results[0].Kind != "refused" || batch.Results[1].Kind != "not_found" {
		t.Fatalf("test login results = %+v", batch)
	}
	var sessions []sessionInfoDTO
	requireStoreTestResponse(t, dispatchHostKeyTest(t, production, "session_list", `null`), &sessions)
	if len(sessions) != 0 {
		t.Fatalf("test login created or reconnected sessions: %+v", sessions)
	}
}

func dispatchTestLoginBatch(t *testing.T, production *Production, timeoutMS int64, assetIDs ...string) assetProbeBatchDTO {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"assetIds": assetIDs, "timeoutMs": timeoutMS})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatchHostKeyTest(t, production, "exec_batch_test_login", `{"args":`+string(payload)+`}`)
	var batch assetProbeBatchDTO
	requireStoreTestResponse(t, response, &batch)
	return batch
}

func testLoginResultsByID(t *testing.T, batch assetProbeBatchDTO) map[string]assetProbeResultDTO {
	t.Helper()
	byID := make(map[string]assetProbeResultDTO, len(batch.Results))
	for _, result := range batch.Results {
		byID[result.AssetID] = result
	}
	return byID
}

func requireNoManagedSessions(t *testing.T, production *Production) {
	t.Helper()
	var sessions []sessionInfoDTO
	requireStoreTestResponse(t, dispatchHostKeyTest(t, production, "session_list", `null`), &sessions)
	if len(sessions) != 0 {
		t.Fatalf("test login registered managed sessions: %+v", sessions)
	}
}

func TestTestLoginIPCRealHandshakeFailureClasses(t *testing.T) {
	server := newSSHConnectorServer(t, nil)
	pendingServer := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	ctx := context.Background()

	goodCredID := createConnectorTestCredential(t, production, "login-good", "secret")
	badCredID := createConnectorTestCredential(t, production, "login-bad", "wrong")
	corruptCredID, err := production.Services.Store.CredentialPut(ctx, store.CredentialInput{
		Name: "login-corrupt", Kind: "password", Nonce: []byte{1}, Blob: []byte("not-an-envelope"),
	})
	if err != nil {
		t.Fatal(err)
	}

	okAssetID := createConnectorTestAsset(t, production, "login-ok", "127.0.0.1", server.port(), "password", goodCredID, "")
	authAssetID := createConnectorTestAsset(t, production, "login-auth", "127.0.0.1", server.port(), "password", badCredID, "")
	pendingAssetID := createConnectorTestAsset(t, production, "login-pending", "127.0.0.1", pendingServer.port(), "password", goodCredID, "")
	vaultAssetID := createConnectorTestAsset(t, production, "login-vault", "127.0.0.1", server.port(), "password", corruptCredID, "")
	configAssetID := createConnectorTestAsset(t, production, "login-config", "127.0.0.1", server.port(), "password", goodCredID,
		`{"jumpAssetId":"missing-jump"}`)
	refusedAssetID := createConnectorTestAsset(t, production, "login-refused", "127.0.0.1", closedProbePort(t), "password", goodCredID, "")
	winrmResponse := dispatchHostKeyTest(t, production, "asset_create",
		`{"args":{"kind":"winrm","name":"login-winrm","host":"127.0.0.1","port":`+itoa(closedProbePort(t))+`,"username":"test"}}`)
	var winrmAsset assetDTO
	requireStoreTestResponse(t, winrmResponse, &winrmAsset)

	byID := testLoginResultsByID(t, dispatchTestLoginBatch(t, production, 5000, okAssetID, pendingAssetID))
	if result := byID[okAssetID]; result.Reachable || result.Kind != "host_key_pending" {
		t.Fatalf("unconfirmed key test login = %+v", result)
	}
	if result := byID[pendingAssetID]; result.Reachable || result.Kind != "host_key_pending" {
		t.Fatalf("unconfirmed pending asset test login = %+v", result)
	}
	if listed := knownHostTestFingerprints(t, production); len(listed) != 0 {
		t.Fatalf("test login persisted trust for unknown keys: %+v", listed)
	}

	var okPending hostKeyProbeDTO
	requireStoreTestResponse(t, dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+okAssetID+`"}}`), &okPending)
	if okPending.State != "pending" {
		t.Fatalf("unknown key probe state = %q", okPending.State)
	}
	acceptHostKeyTest(t, production, hostKeyDetailDTO{
		Host: okPending.Host, Port: okPending.Port, KeyType: okPending.KeyType, Fingerprint: okPending.Fingerprint,
	})

	byID = testLoginResultsByID(t, dispatchTestLoginBatch(t, production, 5000,
		okAssetID, authAssetID, pendingAssetID, vaultAssetID, configAssetID, refusedAssetID, winrmAsset.ID, "missing-asset"))
	if result := byID[okAssetID]; !result.Reachable || result.Kind != "" {
		t.Fatalf("real handshake test login = %+v", result)
	}
	if result := byID[authAssetID]; result.Reachable || result.Kind != "auth" || !strings.Contains(result.Error, "认证失败") {
		t.Fatalf("auth failure test login = %+v", result)
	}
	if result := byID[pendingAssetID]; result.Reachable || result.Kind != "host_key_pending" {
		t.Fatalf("still-unconfirmed asset test login = %+v", result)
	}
	if result := byID[vaultAssetID]; result.Reachable || result.Kind != "vault" || result.Error == "" {
		t.Fatalf("vault failure test login = %+v", result)
	}
	if result := byID[configAssetID]; result.Reachable || result.Kind != "config" {
		t.Fatalf("config failure test login = %+v", result)
	}
	if result := byID[refusedAssetID]; result.Reachable || result.Kind != "refused" {
		t.Fatalf("refused test login = %+v", result)
	}
	if result := byID[winrmAsset.ID]; result.Reachable || result.Kind != "refused" {
		t.Fatalf("winrm TCP fallback test login = %+v", result)
	}
	if result := byID["missing-asset"]; result.Reachable || result.Kind != "not_found" {
		t.Fatalf("missing asset test login = %+v", result)
	}
	requireNoManagedSessions(t, production)
}

func TestTestLoginIPCHostKeyChanged(t *testing.T) {
	server := newHostKeyTestServer(t)
	firstKey := *server.signer.Load()
	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, server.port())

	batch := dispatchTestLoginBatch(t, production, 5000, assetID)
	if len(batch.Results) != 1 || batch.Results[0].Reachable || batch.Results[0].Kind != "host_key_pending" {
		t.Fatalf("unknown key test login = %+v", batch.Results)
	}
	acceptHostKeyTest(t, production, hostKeyDetailDTO{
		Host: "127.0.0.1", Port: server.port(),
		KeyType: firstKey.PublicKey().Type(), Fingerprint: gossh.FingerprintSHA256(firstKey.PublicKey()),
	})
	batch = dispatchTestLoginBatch(t, production, 5000, assetID)
	if len(batch.Results) != 1 || !batch.Results[0].Reachable || batch.Results[0].Kind != "" {
		t.Fatalf("trusted key test login = %+v", batch.Results)
	}

	server.rotateHostKey(t)
	batch = dispatchTestLoginBatch(t, production, 5000, assetID)
	if len(batch.Results) != 1 || batch.Results[0].Reachable || batch.Results[0].Kind != "host_key_changed" {
		t.Fatalf("rotated key test login = %+v", batch.Results)
	}
	if !strings.Contains(batch.Results[0].Error, "不一致") {
		t.Fatalf("changed key error lacks mismatch guidance: %q", batch.Results[0].Error)
	}
	if listed := knownHostTestFingerprints(t, production); len(listed) != 1 || listed[0] != gossh.FingerprintSHA256(firstKey.PublicKey()) {
		t.Fatalf("test login overwrote the confirmed ledger: %+v", listed)
	}
	requireNoManagedSessions(t, production)
}

func TestTestLoginIPCThroughJump(t *testing.T) {
	jump := newSSHConnectorServer(t, nil)
	target := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "login-jump-password", "secret")

	jumpAssetID := createConnectorTestAsset(t, production, "login-jump", "127.0.0.1", jump.port(), "password", credID, "")
	targetAssetID := createConnectorTestAsset(t, production, "login-target", "127.0.0.1", target.port(), "password", credID,
		`{"jumpAssetId":"`+jumpAssetID+`"}`)

	batch := dispatchTestLoginBatch(t, production, 5000, targetAssetID)
	if len(batch.Results) != 1 || batch.Results[0].Reachable || batch.Results[0].Kind != "host_key_pending" {
		t.Fatalf("unconfirmed jump test login = %+v", batch.Results)
	}
	acceptHostKeyTest(t, production, requireHostKeyPending(t,
		dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+targetAssetID+`"}}`), false))
	var targetPending hostKeyProbeDTO
	requireStoreTestResponse(t, dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+targetAssetID+`"}}`), &targetPending)
	if targetPending.State != "pending" || targetPending.Port != target.port() {
		t.Fatalf("target host key probe = %+v, want the target pending state", targetPending)
	}
	acceptHostKeyTest(t, production, hostKeyDetailDTO{
		Host: targetPending.Host, Port: targetPending.Port,
		KeyType: targetPending.KeyType, Fingerprint: targetPending.Fingerprint,
	})

	batch = dispatchTestLoginBatch(t, production, 5000, targetAssetID)
	if len(batch.Results) != 1 || !batch.Results[0].Reachable || batch.Results[0].Kind != "" {
		t.Fatalf("test login through jump = %+v", batch.Results)
	}
	requireNoManagedSessions(t, production)
}

func TestTestLoginIPCTimeout(t *testing.T) {
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "login-timeout", "secret")
	stalling := stallingProxyListener(t)
	slowAssetID := createConnectorTestAsset(t, production, "login-slow", "127.0.0.1", 22, "password", credID,
		`{"proxy":"socks5://`+stalling.Addr().String()+`"}`)

	batch := dispatchTestLoginBatch(t, production, 500, slowAssetID)
	if len(batch.Results) != 1 || batch.Results[0].Reachable || batch.Results[0].Kind != "timeout" {
		t.Fatalf("stalling proxy test login = %+v", batch.Results)
	}
	requireNoManagedSessions(t, production)
}

func TestTestLoginIPCValidation(t *testing.T) {
	production := newHostKeyTestProduction(t)
	response := dispatchHostKeyTest(t, production, "exec_batch_test_login", `{"args":{"assetIds":[]}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("empty batch response = %+v", response)
	}
	oversized := make([]string, maxAssetProbeBatch+1)
	for index := range oversized {
		oversized[index] = "asset"
	}
	payload, err := json.Marshal(map[string]any{"assetIds": oversized})
	if err != nil {
		t.Fatal(err)
	}
	response = dispatchHostKeyTest(t, production, "exec_batch_test_login", `{"args":`+string(payload)+`}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("oversized batch response = %+v", response)
	}
}
