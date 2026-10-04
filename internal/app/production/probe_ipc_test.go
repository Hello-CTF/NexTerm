package production

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	gossh "golang.org/x/crypto/ssh"
)

type sshErrorDetailDTO struct {
	Kind        string `json:"kind"`
	Hint        string `json:"hint"`
	Op          string `json:"op"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Proxy       string `json:"proxy"`
	Jump        string `json:"jump"`
	Changed     bool   `json:"changed"`
	Fingerprint string `json:"fingerprint"`
}

func requireSSHErrorDetail(t *testing.T, response ipc.Response, code ipc.Code, kind string) sshErrorDetailDTO {
	t.Helper()
	if response.OK || response.Error == nil || response.Error.Code != code {
		t.Fatalf("response = %+v, want code %s", response, code)
	}
	encoded, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatalf("error detail is not serializable: %v", err)
	}
	var detail sshErrorDetailDTO
	if err := json.Unmarshal(encoded, &detail); err != nil {
		t.Fatalf("error detail is malformed: %v", err)
	}
	if detail.Kind != kind {
		t.Fatalf("detail kind = %q, want %q: %+v", detail.Kind, kind, detail)
	}
	if detail.Hint == "" {
		t.Fatalf("detail lost the user hint: %+v", detail)
	}
	return detail
}

func createProbeTestAsset(t *testing.T, production *Production, fields string) string {
	t.Helper()
	response := dispatchHostKeyTest(t, production, "asset_create", `{"args":{"kind":"ssh","name":"probe-test",`+fields+`}}`)
	var created assetDTO
	requireStoreTestResponse(t, response, &created)
	return created.ID
}

func stallingProxyListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		listener.Close()
	})
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				<-done
				connection.Close()
			}()
		}
	}()
	return listener
}

func closedProbePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func TestProductionSessionProbeHostKeyTrustTransitions(t *testing.T) {
	server := newHostKeyTestServer(t)
	firstKey := *server.signer.Load()
	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, server.port())

	response := dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+assetID+`"}}`)
	var pending hostKeyProbeDTO
	requireStoreTestResponse(t, response, &pending)
	if pending.State != "pending" {
		t.Fatalf("unknown key probe state = %q", pending.State)
	}
	if pending.Host != "127.0.0.1" || pending.Port != server.port() {
		t.Fatalf("probe endpoint = %+v", pending)
	}
	if pending.KeyType != firstKey.PublicKey().Type() || pending.Fingerprint != gossh.FingerprintSHA256(firstKey.PublicKey()) {
		t.Fatalf("probe lost fingerprint diagnostics: %+v", pending)
	}
	if len(pending.Known) != 0 {
		t.Fatalf("unknown key reported known fingerprints: %+v", pending.Known)
	}
	if listed := knownHostTestFingerprints(t, production); len(listed) != 0 {
		t.Fatalf("probe persisted trust for an unknown key: %+v", listed)
	}

	acceptHostKeyTest(t, production, hostKeyDetailDTO{Host: pending.Host, Port: pending.Port, KeyType: pending.KeyType, Fingerprint: pending.Fingerprint})
	response = dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+assetID+`"}}`)
	var known hostKeyProbeDTO
	requireStoreTestResponse(t, response, &known)
	if known.State != "known" {
		t.Fatalf("recorded key probe state = %q", known.State)
	}

	connected := connectHostKeyTestAsset(t, production, assetID)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))

	secondKey := server.rotateHostKey(t)
	response = dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+assetID+`"}}`)
	var changed hostKeyProbeDTO
	requireStoreTestResponse(t, response, &changed)
	if changed.State != "changed" {
		t.Fatalf("rotated key probe state = %q", changed.State)
	}
	if changed.Fingerprint != gossh.FingerprintSHA256(secondKey) {
		t.Fatalf("changed probe fingerprint = %+v", changed)
	}
	if len(changed.Known) != 1 || changed.Known[0].Fingerprint != pending.Fingerprint {
		t.Fatalf("changed probe lost the previous fingerprint: %+v", changed.Known)
	}
	if listed := knownHostTestFingerprints(t, production); len(listed) != 1 || listed[0] != pending.Fingerprint {
		t.Fatalf("probe overwrote the confirmed ledger: %+v", listed)
	}
}

func TestProductionSessionProbeHostKeyValidation(t *testing.T) {
	production := newHostKeyTestProduction(t)

	response := dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("empty asset id response = %+v", response)
	}

	response = dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"missing-asset"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("missing asset response = %+v", response)
	}

	localResponse := dispatchHostKeyTest(t, production, "asset_create", `{"args":{"kind":"local","name":"probe-local"}}`)
	var local assetDTO
	requireStoreTestResponse(t, localResponse, &local)
	response = dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+local.ID+`"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("local asset probe response = %+v", response)
	}
}

func TestProductionSessionProbeHostKeyRefused(t *testing.T) {
	production := newHostKeyTestProduction(t)
	assetID := createProbeTestAsset(t, production, `"host":"127.0.0.1","port":`+itoa(closedProbePort(t))+`,"username":"test","authKind":"password"`)

	response := dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+assetID+`"}}`)
	detail := requireSSHErrorDetail(t, response, ipc.CodeSSH, "refused")
	if detail.Op != "dial" || detail.Host != "127.0.0.1" {
		t.Fatalf("refused probe detail = %+v", detail)
	}
}

func TestProductionAssetProbeBatchResults(t *testing.T) {
	server := newHostKeyTestServer(t)
	production := newHostKeyTestProduction(t)
	reachableID := createHostKeyTestAsset(t, production, server.port())
	refusedID := createProbeTestAsset(t, production, `"host":"127.0.0.1","port":`+itoa(closedProbePort(t))+`,"username":"test","authKind":"password"`)
	localResponse := dispatchHostKeyTest(t, production, "asset_create", `{"args":{"kind":"local","name":"batch-local"}}`)
	var local assetDTO
	requireStoreTestResponse(t, localResponse, &local)

	payload, err := json.Marshal(map[string]any{"assetIds": []string{reachableID, refusedID, "missing-asset", local.ID, reachableID}})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatchHostKeyTest(t, production, "asset_probe_batch", `{"args":`+string(payload)+`}`)
	var batch assetProbeBatchDTO
	requireStoreTestResponse(t, response, &batch)
	if len(batch.Results) != 4 {
		t.Fatalf("batch results = %+v", batch.Results)
	}
	byID := make(map[string]assetProbeResultDTO, len(batch.Results))
	for _, result := range batch.Results {
		byID[result.AssetID] = result
	}
	if result := byID[reachableID]; !result.Reachable || result.Kind != "" {
		t.Fatalf("reachable asset result = %+v", result)
	}
	if result := byID[refusedID]; result.Reachable || result.Kind != "refused" || result.Error == "" {
		t.Fatalf("refused asset result = %+v", result)
	}
	if result := byID["missing-asset"]; result.Reachable || result.Kind != "not_found" {
		t.Fatalf("missing asset result = %+v", result)
	}
	if result := byID[local.ID]; !result.Reachable {
		t.Fatalf("local asset result = %+v", result)
	}
	if batch.Results[0].AssetID != reachableID || batch.Results[1].AssetID != refusedID || batch.Results[2].AssetID != "missing-asset" || batch.Results[3].AssetID != local.ID {
		t.Fatalf("batch results lost input order: %+v", batch.Results)
	}
}

func TestProductionSessionProbeHostKeyThroughJump(t *testing.T) {
	jump := newSSHConnectorServer(t, nil)
	target := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "jump-probe-password", "secret")

	jumpAssetID := createConnectorTestAsset(t, production, "jump-probe", "127.0.0.1", jump.port(), "password", credID, "")
	targetAssetID := createConnectorTestAsset(t, production, "target-probe", "127.0.0.1", target.port(), "password", credID,
		`{"jumpAssetId":"`+jumpAssetID+`"}`)

	response := dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+targetAssetID+`"}}`)
	jumpPending := requireHostKeyPending(t, response, false)
	if jumpPending.Port != jump.port() {
		t.Fatalf("first pending endpoint = %+v, want the jump hop", jumpPending)
	}
	if listed := knownHostTestFingerprints(t, production); len(listed) != 0 {
		t.Fatalf("probe persisted trust for the jump key: %+v", listed)
	}
	acceptHostKeyTest(t, production, jumpPending)

	response = dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+targetAssetID+`"}}`)
	var targetPending hostKeyProbeDTO
	requireStoreTestResponse(t, response, &targetPending)
	if targetPending.State != "pending" || targetPending.Port != target.port() {
		t.Fatalf("second probe = %+v, want the target pending state", targetPending)
	}
	if listed := knownHostTestFingerprints(t, production); len(listed) != 1 {
		t.Fatalf("probe persisted trust for the target key: %+v", listed)
	}
	acceptHostKeyTest(t, production, hostKeyDetailDTO{Host: targetPending.Host, Port: targetPending.Port, KeyType: targetPending.KeyType, Fingerprint: targetPending.Fingerprint})

	response = dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+targetAssetID+`"}}`)
	var known hostKeyProbeDTO
	requireStoreTestResponse(t, response, &known)
	if known.State != "known" || known.Port != target.port() {
		t.Fatalf("probe through jump = %+v", known)
	}
}

func TestProductionSessionProbeHostKeyJumpFailures(t *testing.T) {
	jump := newSSHConnectorServer(t, nil)
	target := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	rightCredID := createConnectorTestCredential(t, production, "right-jump-password", "secret")
	wrongCredID := createConnectorTestCredential(t, production, "wrong-jump-password", "wrong")

	jumpAssetID := createConnectorTestAsset(t, production, "jump-auth", "127.0.0.1", jump.port(), "password", wrongCredID, "")
	targetAssetID := createConnectorTestAsset(t, production, "target-auth", "127.0.0.1", target.port(), "password", rightCredID,
		`{"jumpAssetId":"`+jumpAssetID+`"}`)

	response := dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+targetAssetID+`"}}`)
	acceptHostKeyTest(t, production, requireHostKeyPending(t, response, false))
	response = dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+targetAssetID+`"}}`)
	detail := requireSSHErrorDetail(t, response, ipc.CodeSSH, "auth")
	if detail.Host != "127.0.0.1" || detail.Port != jump.port() {
		t.Fatalf("jump auth failure detail = %+v", detail)
	}

	jumpAssetID = createConnectorTestAsset(t, production, "jump-dead", "127.0.0.1", jump.port(), "password", rightCredID, "")
	targetAssetID = createConnectorTestAsset(t, production, "target-dead", "127.0.0.1", closedProbePort(t), "password", rightCredID,
		`{"jumpAssetId":"`+jumpAssetID+`"}`)
	response = dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+targetAssetID+`"}}`)
	detail = requireSSHErrorDetail(t, response, ipc.CodeSSH, "refused")
	if detail.Op != "jump dial" || detail.Jump != "127.0.0.1:"+strconv.Itoa(jump.port()) {
		t.Fatalf("jump dial failure detail = %+v", detail)
	}
}

func TestProductionAssetProbeBatchThroughJump(t *testing.T) {
	jump := newSSHConnectorServer(t, nil)
	target := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "batch-jump-password", "secret")

	jumpAssetID := createConnectorTestAsset(t, production, "jump-batch", "127.0.0.1", jump.port(), "password", credID,
		`{"autoAcceptUnknownHost":true}`)
	targetAssetID := createConnectorTestAsset(t, production, "target-batch", "127.0.0.1", target.port(), "password", credID,
		`{"jumpAssetId":"`+jumpAssetID+`","autoAcceptUnknownHost":true}`)
	deadAssetID := createConnectorTestAsset(t, production, "target-batch-dead", "127.0.0.1", closedProbePort(t), "password", credID,
		`{"jumpAssetId":"`+jumpAssetID+`","autoAcceptUnknownHost":true}`)

	payload, err := json.Marshal(map[string]any{"assetIds": []string{targetAssetID, deadAssetID}})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatchHostKeyTest(t, production, "asset_probe_batch", `{"args":`+string(payload)+`}`)
	var pending assetProbeBatchDTO
	requireStoreTestResponse(t, response, &pending)
	if len(pending.Results) != 2 || pending.Results[0].Kind != "host_key_pending" || pending.Results[1].Kind != "host_key_pending" {
		t.Fatalf("batch through unconfirmed jump = %+v", pending.Results)
	}

	connected := connectHostKeyTestAsset(t, production, targetAssetID)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
	response = dispatchHostKeyTest(t, production, "asset_probe_batch", `{"args":`+string(payload)+`}`)
	var batch assetProbeBatchDTO
	requireStoreTestResponse(t, response, &batch)
	if len(batch.Results) != 2 {
		t.Fatalf("batch results = %+v", batch.Results)
	}
	if result := batch.Results[0]; !result.Reachable || result.AssetID != targetAssetID {
		t.Fatalf("reachable through jump = %+v", result)
	}
	if result := batch.Results[1]; result.Reachable || result.Kind != "refused" || !strings.Contains(result.Error, "through jump host") {
		t.Fatalf("unreachable through jump = %+v", result)
	}
}

func TestProductionAssetProbeBatchValidation(t *testing.T) {
	production := newHostKeyTestProduction(t)
	response := dispatchHostKeyTest(t, production, "asset_probe_batch", `{"args":{"assetIds":[]}}`)
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
	response = dispatchHostKeyTest(t, production, "asset_probe_batch", `{"args":`+string(payload)+`}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("oversized batch response = %+v", response)
	}
}

func TestProductionAssetProbeBatchCancellation(t *testing.T) {
	production := newHostKeyTestProduction(t)
	stalling := stallingProxyListener(t)
	slowID := createProbeTestAsset(t, production, `"host":"127.0.0.1","port":22,"username":"test","authKind":"password","options":{"proxy":"socks5://`+stalling.Addr().String()+`"}`)
	fastID := createProbeTestAsset(t, production, `"host":"127.0.0.1","port":`+itoa(closedProbePort(t))+`,"username":"test","authKind":"password"`)

	payload, err := json.Marshal(map[string]any{"assetIds": []string{slowID, fastID}, "timeoutMs": 30000, "maxConcurrent": 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)
	started := time.Now()
	response := production.Dispatcher.Dispatch(ctx, ipc.Request{
		Command: "asset_probe_batch", Args: json.RawMessage(`{"args":` + string(payload) + `}`), ClientID: "client-a",
	}, production.Environment("client-a"))
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("canceled batch took %v", elapsed)
	}
	var batch assetProbeBatchDTO
	requireStoreTestResponse(t, response, &batch)
	if len(batch.Results) != 2 {
		t.Fatalf("canceled batch results = %+v", batch.Results)
	}
	for _, result := range batch.Results {
		if result.Reachable || result.Kind != "canceled" {
			t.Fatalf("canceled batch result = %+v", result)
		}
	}
}

func TestProductionSessionConnectErrorClassification(t *testing.T) {
	server := newHostKeyTestServer(t)
	production := newHostKeyTestProduction(t)

	refusedID := createProbeTestAsset(t, production, `"host":"127.0.0.1","port":`+itoa(closedProbePort(t))+`,"username":"test","authKind":"password"`)
	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+refusedID+`"}}`)
	detail := requireSSHErrorDetail(t, response, ipc.CodeSSH, "refused")
	if detail.Op != "dial" || detail.Host != "127.0.0.1" || detail.Port == 0 {
		t.Fatalf("refused connect detail = %+v", detail)
	}

	dnsID := createProbeTestAsset(t, production, `"host":"nonexistent.invalid.","port":22,"username":"test","authKind":"password"`)
	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+dnsID+`"}}`)
	requireSSHErrorDetail(t, response, ipc.CodeSSH, "dns")

	authID := createProbeTestAsset(t, production, `"host":"127.0.0.1","port":`+itoa(server.port())+`,"username":"intruder","authKind":"password"`)
	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+authID+`","acceptHostKey":true}}`)
	requireSSHErrorDetail(t, response, ipc.CodeSSH, "auth")

	stalling := stallingProxyListener(t)
	timeoutID := createProbeTestAsset(t, production, `"host":"127.0.0.1","port":`+strconv.Itoa(stalling.Addr().(*net.TCPAddr).Port)+`,"username":"test","authKind":"password","options":{"connectTimeout":1}`)
	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+timeoutID+`"}}`)
	requireSSHErrorDetail(t, response, ipc.CodeTimeout, "timeout")

	proxyID := createProbeTestAsset(t, production, `"host":"127.0.0.1","port":22,"username":"test","authKind":"password","options":{"proxy":"socks5://proxyuser:topsecret@127.0.0.1:1"}`)
	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+proxyID+`"}}`)
	detail = requireSSHErrorDetail(t, response, ipc.CodeSSH, "refused")
	if detail.Op != "proxy connect" || detail.Proxy != "socks5://127.0.0.1:1" {
		t.Fatalf("proxy connect detail = %+v", detail)
	}
	if strings.Contains(response.Error.Message, "topsecret") || strings.Contains(response.Error.Message, "proxyuser") {
		t.Fatalf("proxy credentials leaked into IPC error: %v", response.Error.Message)
	}

	pendingServer := newHostKeyTestServer(t)
	pendingID := createHostKeyTestAsset(t, production, pendingServer.port())
	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+pendingID+`"}}`)
	detail = requireSSHErrorDetail(t, response, ipc.CodeHostKeyPending, "host_key_pending")
	if detail.Fingerprint == "" || detail.Host != "127.0.0.1" || detail.Port != pendingServer.port() {
		t.Fatalf("host key pending detail = %+v", detail)
	}
	if detail.Changed {
		t.Fatalf("unknown key reported as changed: %+v", detail)
	}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
