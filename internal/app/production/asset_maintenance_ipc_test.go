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
	port := int32(closedProbePort(t))
	host := "127.0.0.1"
	asset, err := production.Services.Store.AssetCreate(context.Background(), store.AssetInput{
		Kind: "ssh", Name: "refused", Host: &host, Port: &port, OptionsJSON: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"assetIds": []string{asset.ID, "missing", asset.ID}, "timeoutMs": 500})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatchHostKeyTest(t, production, "exec_batch_test_login", `{"args":`+string(payload)+`}`)
	var batch assetProbeBatchDTO
	requireStoreTestResponse(t, response, &batch)
	if len(batch.Results) != 2 || batch.Results[0].AssetID != asset.ID || batch.Results[1].AssetID != "missing" {
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
