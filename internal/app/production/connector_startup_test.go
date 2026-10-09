package production

import (
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestProductionStartupCommandRejectedForNonSSHAssets(t *testing.T) {
	server := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "startup-password", "secret")

	winrmResponse := dispatchHostKeyTest(t, production, "asset_create",
		`{"args":{"kind":"winrm","name":"winrm-startup","host":"127.0.0.1","port":5985,"username":"test","options":{"startupCommand":"echo hi"}}}`)
	var winrmAsset assetDTO
	requireStoreTestResponse(t, winrmResponse, &winrmAsset)
	requireConnectorConnectError(t, production, winrmAsset.ID, "启动命令仅支持 SSH 资产")

	localResponse := dispatchHostKeyTest(t, production, "asset_create",
		`{"args":{"kind":"local","name":"local-startup","options":{"startupCommand":"echo hi"}}}`)
	var localAsset assetDTO
	requireStoreTestResponse(t, localResponse, &localAsset)
	requireConnectorConnectError(t, production, localAsset.ID, "启动命令仅支持 SSH 资产")

	sshID := createConnectorTestAsset(t, production, "ssh-startup", "127.0.0.1", server.port(), "password", credID,
		`{"startupCommand":"echo hi","autoAcceptUnknownHost":true}`)
	connected := connectHostKeyTestAsset(t, production, sshID)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
}

func TestProductionAssetEncodingValidatedAtConnect(t *testing.T) {
	server := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "encoding-password", "secret")

	invalidID := createConnectorTestAsset(t, production, "bad-encoding", "127.0.0.1", server.port(), "password", credID,
		`{"encoding":"klingon"}`)
	requireConnectorConnectError(t, production, invalidID, "资产终端编码无效")

	validID := createConnectorTestAsset(t, production, "gbk-encoding", "127.0.0.1", server.port(), "password", credID,
		`{"encoding":"gbk","autoAcceptUnknownHost":true}`)
	connected := connectHostKeyTestAsset(t, production, validID)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
}

func TestProductionSSHStartupCommandEnvAndEncodingEndToEnd(t *testing.T) {
	server := newSSHConnectorServer(t, nil)
	server.envReqs = make(chan [2]string, 4)
	server.shellInput = make(chan string, 4)
	production := newTerminalTestProduction(t, durableTestDataDir(t), &bridgeTestFactory{}, nil)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "startup-e2e-password", "secret")
	assetID := createConnectorTestAsset(t, production, "startup-e2e", "127.0.0.1", server.port(), "password", credID,
		`{"autoAcceptUnknownHost":true,"encoding":"gbk","startupCommand":"echo hello","env":{"NEXTERM_TEST":"1"}}`)

	connectedResponse := dispatchDurableTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`, "", "client-a")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)

	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "startup-e2e-channel", "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	if tabID == "" {
		t.Fatal("terminal_attach returned no tab")
	}

	select {
	case env := <-server.envReqs:
		if env != [2]string{"NEXTERM_TEST", "1"} {
			t.Fatalf("env request = %v, want NEXTERM_TEST=1", env)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("asset env was not delivered over SSH")
	}
	select {
	case input := <-server.shellInput:
		if input != "echo hello\n" {
			t.Fatalf("shell input = %q, want the startup command", input)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup command was not written into the terminal")
	}

	attachTabResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":1024}`, "startup-e2e-reattach", "client-a")
	var attached attachedTabDTO
	requireStoreTestResponse(t, attachTabResponse, &attached)
	if attached.Encoding != "gbk" {
		t.Fatalf("attached tab encoding = %q, want gbk", attached.Encoding)
	}

	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
}

func TestProductionAssetOptionMapKeepsStartupKeys(t *testing.T) {
	row := store.AssetRow{ID: "options-row", Kind: "ssh", Name: "options", OptionsJSON: `{"startupCommand":"echo hi","encoding":"gbk","env":{"A":"1"}}`}
	options, err := productionAssetOptionMap(row)
	if err != nil {
		t.Fatal(err)
	}
	if options["startupCommand"] != "echo hi" || options["encoding"] != "gbk" {
		t.Fatalf("options = %+v", options)
	}
	env, ok := options["env"].(map[string]any)
	if !ok || env["A"] != "1" {
		t.Fatalf("options env = %+v", options["env"])
	}
	asset, err := productionSessionAsset(row, false)
	if err != nil {
		t.Fatal(err)
	}
	if asset.StartupCommand != "echo hi" || asset.Encoding != "gbk" {
		t.Fatalf("asset = %+v", asset)
	}
}
