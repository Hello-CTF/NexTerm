package production

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

func createInheritanceGroup(t *testing.T, production *Production, name, defaults string) string {
	t.Helper()
	response := dispatchHostKeyTest(t, production, "group_create", `{"name":"`+name+`","defaults":`+defaults+`}`)
	var group groupDTO
	requireStoreTestResponse(t, response, &group)
	return group.ID
}

func createInheritingAsset(t *testing.T, production *Production, name, kind, groupID, host string, port int, options string) string {
	t.Helper()
	args := `{"kind":"` + kind + `","name":"` + name + `","groupId":"` + groupID + `","host":"` + host + `","port":` + jsonInt(port)
	if options != "" {
		args += `,"options":` + options
	}
	args += `}`
	response := dispatchHostKeyTest(t, production, "asset_create", `{"args":`+args+`}`)
	var created assetDTO
	requireStoreTestResponse(t, response, &created)
	return created.ID
}

func jsonInt(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestGroupInheritanceConnectsThroughInheritedJumpAndAppliesEdits(t *testing.T) {
	jump := newSSHConnectorServer(t, nil)
	target := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "group-password", "secret")
	jumpAssetID := createConnectorTestAsset(t, production, "group-jump", "127.0.0.1", jump.port(), "password", credID,
		`{"autoAcceptUnknownHost":true}`)
	groupID := createInheritanceGroup(t, production, "生产环境",
		`{"username":"test","authKind":"password","credId":"`+credID+`","jumpAssetId":"`+jumpAssetID+`"}`)
	memberID := createInheritingAsset(t, production, "member", "ssh", groupID, "127.0.0.1", target.port(), `{"autoAcceptUnknownHost":true}`)

	// The member stores no username, authentication or jump of its own.
	stored, err := production.Services.Store.AssetGet(t.Context(), memberID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Username != nil || stored.AuthKind != nil || stored.CredID != nil || strings.Contains(stored.OptionsJSON, "jumpAssetId") {
		t.Fatalf("member holds its own connection values: %+v", stored)
	}

	connected := connectHostKeyTestAsset(t, production, memberID)
	if connected.Status != "connected" {
		t.Fatalf("session = %+v", connected)
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))

	// An edit of the group reaches the next connection without touching the asset.
	response := dispatchHostKeyTest(t, production, "group_update", `{"id":"`+groupID+`","defaults":{"username":"nobody","authKind":"password","credId":"`+credID+`","jumpAssetId":"`+jumpAssetID+`"}}`)
	var updated groupDTO
	requireStoreTestResponse(t, response, &updated)
	if updated.DefaultUsername == nil || *updated.DefaultUsername != "nobody" {
		t.Fatalf("group = %+v", updated)
	}
	failed := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+memberID+`"}}`)
	if failed.OK {
		t.Fatal("the edited group username was not used for the next connection")
	}
	text, _ := json.Marshal(failed)
	if !strings.Contains(string(text), "生产环境") {
		t.Fatalf("connection error does not name the group: %s", text)
	}
}

func TestGroupInheritanceJumpCycleIsDetected(t *testing.T) {
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "cycle-password", "secret")
	groupA := createInheritanceGroup(t, production, "组A", `{"username":"test","authKind":"password","credId":"`+credID+`"}`)
	groupB := createInheritanceGroup(t, production, "组B", `{"username":"test","authKind":"password","credId":"`+credID+`"}`)
	a := createInheritingAsset(t, production, "a", "ssh", groupA, "127.0.0.1", 1, "")
	b := createInheritingAsset(t, production, "b", "ssh", groupB, "127.0.0.1", 2, "")
	// Each group sends its members through the other group's asset.
	for _, update := range []struct{ group, jump string }{{groupA, b}, {groupB, a}} {
		response := dispatchHostKeyTest(t, production, "group_update", `{"id":"`+update.group+`","defaults":{"username":"test","authKind":"password","credId":"`+credID+`","jumpAssetId":"`+update.jump+`"}}`)
		if !response.OK {
			t.Fatalf("group_update: %+v", response)
		}
	}
	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+a+`"}}`)
	text, _ := json.Marshal(response)
	if response.OK || !strings.Contains(string(text), "jump asset cycle") {
		t.Fatalf("an inherited jump cycle was not detected: %s", text)
	}
}

func TestGroupInheritanceProbeUsesInheritedJump(t *testing.T) {
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "probe-password", "secret")
	jumpPort := closedProbePort(t)
	jumpAssetID := createConnectorTestAsset(t, production, "probe-jump", "127.0.0.1", jumpPort, "password", credID,
		`{"autoAcceptUnknownHost":true}`)
	groupID := createInheritanceGroup(t, production, "探测组",
		`{"username":"test","authKind":"password","credId":"`+credID+`","jumpAssetId":"`+jumpAssetID+`"}`)
	memberID := createInheritingAsset(t, production, "probe-member", "ssh", groupID, "127.0.0.1", closedProbePort(t), "")

	response := dispatchHostKeyTest(t, production, "asset_probe_batch", `{"args":{"assetIds":["`+memberID+`"],"timeoutMs":2000}}`)
	var batch assetProbeBatchDTO
	requireStoreTestResponse(t, response, &batch)
	if len(batch.Results) != 1 || batch.Results[0].Reachable || !strings.Contains(batch.Results[0].Error, ":"+jsonInt(jumpPort)) {
		t.Fatalf("the probe did not go through the inherited jump host: %+v", batch.Results)
	}
}

func TestGroupInheritanceProbeReportsUnusableGroupDefault(t *testing.T) {
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "gone-password", "secret")
	groupID := createInheritanceGroup(t, production, "坏默认值", `{"authKind":"password","credId":"`+credID+`"}`)
	memberID := createInheritingAsset(t, production, "broken-member", "ssh", groupID, "127.0.0.1", 22, "")
	if err := production.Services.Store.GORM().Exec("DELETE FROM credential WHERE id = ?", credID).Error; err != nil {
		t.Fatal(err)
	}
	response := dispatchHostKeyTest(t, production, "asset_probe_batch", `{"args":{"assetIds":["`+memberID+`"],"timeoutMs":2000}}`)
	var batch assetProbeBatchDTO
	requireStoreTestResponse(t, response, &batch)
	if len(batch.Results) != 1 || !strings.Contains(batch.Results[0].Error, "坏默认值") {
		t.Fatalf("the probe error does not name the group: %+v", batch.Results)
	}
}

func TestGroupInheritanceCronAndFingerprintReadResolvedValues(t *testing.T) {
	production := newHostKeyTestProduction(t)
	ctx := context.Background()
	database := production.Services.Store
	credential, err := database.CredentialPut(ctx, store.CredentialInput{Name: "cron-cred", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	group, err := database.GroupCreate(ctx, store.GroupInput{Name: "cron", Defaults: &store.GroupDefaults{AuthKind: "password", CredID: credential}})
	if err != nil {
		t.Fatal(err)
	}
	host := "127.0.0.1"
	member, err := database.AssetCreate(ctx, store.AssetInput{GroupID: &group.ID, Kind: "ssh", Name: "cron-member", Host: &host, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	initConnectorTestVault(t, production)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "vault_lock", `null`))
	evidence := cronConnectEvidence{services: &production.Services}
	ready, err := evidence.VaultReady(ctx, cron.Trigger{}, member.ID)
	if err != nil || ready {
		t.Fatalf("a locked vault was reported ready for an inherited credential: ready=%v err=%v", ready, err)
	}

	before, err := database.AssetGetLiveResolved(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.GroupUpdate(ctx, group.ID, store.GroupPatch{Defaults: &store.GroupDefaults{Username: "someone", AuthKind: "password", CredID: credential}}); err != nil {
		t.Fatal(err)
	}
	after, err := database.AssetGetLiveResolved(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if productionAssetConfigFingerprint(before.AssetRow) == productionAssetConfigFingerprint(after.AssetRow) {
		t.Fatal("a group edit did not change the session config fingerprint, so open sessions would not be marked stale")
	}
}

func TestGroupInheritancePreviewCommand(t *testing.T) {
	production := newHostKeyTestProduction(t)
	groupID := createInheritanceGroup(t, production, "预览组", `{"username":"deploy","authKind":"agent"}`)
	response := dispatchHostKeyTest(t, production, "asset_inheritance_preview", `{"kind":"ssh","groupId":"`+groupID+`"}`)
	var preview assetInheritanceDTO
	requireStoreTestResponse(t, response, &preview)
	applied := map[string]string{}
	for _, field := range preview.Fields {
		if field.Applied {
			applied[field.Field] = field.GroupName
		}
	}
	if applied["username"] != "预览组" || applied["auth"] != "预览组" {
		t.Fatalf("preview = %+v", preview)
	}
	// An asset's own value is not previewed as inherited.
	response = dispatchHostKeyTest(t, production, "asset_inheritance_preview", `{"kind":"ssh","groupId":"`+groupID+`","username":"me","authKind":"password"}`)
	requireStoreTestResponse(t, response, &preview)
	if len(preview.Fields) != 0 {
		t.Fatalf("own values previewed as inherited: %+v", preview)
	}
}

func TestSSHImportDuplicateMatchUsesEffectiveUsername(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	group, err := database.GroupCreate(ctx, store.GroupInput{Name: "g", Defaults: &store.GroupDefaults{Username: "deploy"}})
	if err != nil {
		t.Fatal(err)
	}
	host := "web.example.com"
	if _, err := database.AssetCreate(ctx, store.AssetInput{GroupID: &group.ID, Kind: "ssh", Name: "web", Host: &host, OptionsJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	planner := &sshImportPlanner{database: database, vault: vault.Load(ctx, database), desktop: true}
	existing, err := planner.loadExisting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(existing.assets) != 1 || existing.assets[0].Username != "deploy" {
		t.Fatalf("existing assets = %+v", existing.assets)
	}
}

func TestGroupInheritanceCredentialRequiredCommand(t *testing.T) {
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "need-cred", "secret")
	groupID := createInheritanceGroup(t, production, "需要凭据", `{"authKind":"password","credId":"`+credID+`"}`)
	member := createInheritingAsset(t, production, "needy", "ssh", groupID, "127.0.0.1", 22, "")
	var answer assetCredentialRequiredDTO
	requireStoreTestResponse(t, dispatchHostKeyTest(t, production, "asset_credential_required", `{"id":"`+member+`"}`), &answer)
	if !answer.Required {
		t.Fatal("an asset inheriting a credential reported that it needs none")
	}
	plain := createInheritingAsset(t, production, "agent-asset", "ssh", groupID, "127.0.0.1", 22, "")
	requireStoreTestResponse(t, dispatchHostKeyTest(t, production, "asset_update", `{"args":{"id":"`+plain+`","authKind":"agent"}}`), &assetDTO{})
	requireStoreTestResponse(t, dispatchHostKeyTest(t, production, "asset_credential_required", `{"id":"`+plain+`"}`), &answer)
	if answer.Required {
		t.Fatal("an asset with its own agent authentication reported a credential")
	}
}

// WinRM has no real "no password" mode: the connector sends whatever credential is resolved, and an empty one is an
// empty password. The explicit none choice is therefore the only way a group password stays off a WinRM asset.
func TestWinRMExplicitNoneKeepsGroupPasswordOff(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	cred, err := database.CredentialPut(ctx, store.CredentialInput{Name: "win", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	group, err := database.GroupCreate(ctx, store.GroupInput{Name: "win", Defaults: &store.GroupDefaults{Username: "Administrator", AuthKind: "password", CredID: cred}})
	if err != nil {
		t.Fatal(err)
	}
	host := "win.example.com"
	none := store.AuthKindNone
	optionsFor := func(authKind *string) productionAssetOptions {
		row, err := database.AssetCreate(ctx, store.AssetInput{GroupID: &group.ID, Kind: "winrm", Name: "w" + host, Host: &host, AuthKind: authKind, OptionsJSON: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := database.ResolveAsset(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		options, err := productionAssetOptionsFromRow(resolved)
		if err != nil {
			t.Fatal(err)
		}
		return options
	}
	if inherited := optionsFor(nil); inherited.CredID != cred || inherited.Username != "Administrator" || inherited.InheritedFrom[store.InheritAuth] != "win" {
		t.Fatalf("inheriting winrm options = %+v", inherited)
	}
	if explicit := optionsFor(&none); explicit.CredID != "" || explicit.InheritedFrom[store.InheritAuth] != "" {
		t.Fatalf("a winrm asset set to none picked up the group password: %+v", explicit)
	}
}
