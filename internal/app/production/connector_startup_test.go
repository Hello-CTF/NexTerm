package production

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
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
	requireConnectorConnectError(t, production, winrmAsset.ID, "仅 SSH 资产支持启动命令")

	localResponse := dispatchHostKeyTest(t, production, "asset_create",
		`{"args":{"kind":"local","name":"local-startup","options":{"startupCommand":"echo hi"}}}`)
	var localAsset assetDTO
	requireStoreTestResponse(t, localResponse, &localAsset)
	requireConnectorConnectError(t, production, localAsset.ID, "仅 SSH 资产支持启动命令")

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
