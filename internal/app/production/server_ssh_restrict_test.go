package production

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

func requireUnsupported(t *testing.T, err error, field string) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeUnsupported {
		t.Fatalf("%s: err = %v, want unsupported", field, err)
	}
}

var serverRestrictedOptions = map[string]string{
	"proxyCommand": `{"proxyCommand":"touch marker"}`,
	"agentSocket":  `{"agentSocket":"/tmp/agent.sock"}`,
	"forwardAgent": `{"forwardAgent":true}`,
	"authKind":     `{"authKind":"agent"}`,
	"keyPath":      `{"keyPath":"/etc/ssh/ssh_host_ed25519_key"}`,
	"certPath":     `{"certPath":"/etc/ssh/cert.pub"}`,
}

func TestMultiUserAssetWritesRejectServerSSHOptions(t *testing.T) {
	database := newKnownHostOwnerTestDB(t)
	owner := ipc.WithUserID(t.Context(), "user-a")
	host := "example.com"
	for field, options := range serverRestrictedOptions {
		_, err := database.AssetCreate(owner, store.AssetInput{Kind: "ssh", Name: field, Host: &host, OptionsJSON: options})
		requireUnsupported(t, err, "create "+field)
	}
	agent := "agent"
	_, err := database.AssetCreate(owner, store.AssetInput{Kind: "ssh", Name: "agent", Host: &host, AuthKind: &agent, OptionsJSON: "{}"})
	requireUnsupported(t, err, "create authKind column")
	keyPath := "/root/.ssh/id"
	_, err = database.AssetCreate(owner, store.AssetInput{Kind: "ssh", Name: "kp", Host: &host, KeyPath: &keyPath, OptionsJSON: "{}"})
	requireUnsupported(t, err, "create keyPath column")

	pw := "password"
	row, err := database.AssetCreate(owner, store.AssetInput{Kind: "ssh", Name: "ok", Host: &host, AuthKind: &pw, OptionsJSON: `{"proxy":"socks5://p:1080"}`})
	if err != nil {
		t.Fatal(err)
	}
	for field, options := range serverRestrictedOptions {
		options := options
		_, err := database.AssetUpdate(owner, row.ID, store.AssetPatch{OptionsJSON: &options})
		requireUnsupported(t, err, "update "+field)
	}
	_, err = database.AssetUpdate(owner, row.ID, store.AssetPatch{AuthKind: store.Value("agent")})
	requireUnsupported(t, err, "update authKind column")
	_, err = database.AssetUpdate(owner, row.ID, store.AssetPatch{KeyPath: store.Value("/x")})
	requireUnsupported(t, err, "update keyPath column")

	// Desktop (no owner) is unchanged.
	for field, options := range serverRestrictedOptions {
		if _, err := database.AssetCreate(t.Context(), store.AssetInput{Kind: "ssh", Name: "d-" + field, Host: &host, OptionsJSON: options}); err != nil {
			t.Fatalf("desktop %s: %v", field, err)
		}
	}
}

func TestMultiUserAssetUpdateIgnoresLegacyFieldsOnRename(t *testing.T) {
	database := newKnownHostOwnerTestDB(t)
	owner := ipc.WithUserID(t.Context(), "user-a")
	host := "example.com"
	// Legacy row written before the restriction (no owner ctx, owner assigned afterwards).
	row, err := database.AssetCreate(t.Context(), store.AssetInput{Kind: "ssh", Name: "old", Host: &host, OptionsJSON: `{"proxyCommand":"x"}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := func() error {
		_, err := database.DB().Exec("UPDATE asset SET owner_id = ? WHERE id = ?", "user-a", row.ID)
		return err
	}(); err != nil {
		t.Fatal(err)
	}
	name := "renamed"
	if _, err := database.AssetUpdate(owner, row.ID, store.AssetPatch{Name: &name}); err != nil {
		t.Fatalf("rename of legacy asset: %v", err)
	}
}

func TestMultiUserGroupWritesRejectServerSSHDefaults(t *testing.T) {
	database := newKnownHostOwnerTestDB(t)
	owner := ipc.WithUserID(t.Context(), "user-a")
	_, err := database.GroupCreate(owner, store.GroupInput{Name: "g1", Defaults: &store.GroupDefaults{AuthKind: "agent"}})
	requireUnsupported(t, err, "group create agent")
	_, err = database.GroupCreate(owner, store.GroupInput{Name: "g2", Defaults: &store.GroupDefaults{AuthKind: "key", KeyPath: "/root/id"}})
	requireUnsupported(t, err, "group create keyPath")
	group, err := database.GroupCreate(owner, store.GroupInput{Name: "g3"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.GroupUpdate(owner, group.ID, store.GroupPatch{Defaults: &store.GroupDefaults{AuthKind: "agent"}})
	requireUnsupported(t, err, "group update agent")
	if _, err := database.GroupCreate(t.Context(), store.GroupInput{Name: "d", Defaults: &store.GroupDefaults{AuthKind: "agent", KeyPath: "/k"}}); err != nil {
		t.Fatalf("desktop group: %v", err)
	}
}

func TestMultiUserAssetCreateDispatchRejectsProxyCommand(t *testing.T) {
	database := newKnownHostOwnerTestDB(t)
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	args := `{"args":{"kind":"ssh","name":"p","host":"example.com","options":{"proxyCommand":"id"}}}`
	owner := ipc.WithUserID(t.Context(), "user-a")
	response := dispatcher.Dispatch(owner, ipc.Request{Command: "asset_create", Args: json.RawMessage(args)}, ipc.Environment{})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("response = %+v, want unsupported", response)
	}
	response = dispatcher.Dispatch(t.Context(), ipc.Request{Command: "asset_create", Args: json.RawMessage(args)}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("desktop response = %+v", response)
	}
}

func legacySSHAsset(t *testing.T, database *store.Store, name, options string, authKind, keyPath *string, groupID *string) store.AssetRow {
	t.Helper()
	host := "127.0.0.1"
	port := int32(1)
	row, err := database.AssetCreate(t.Context(), store.AssetInput{
		Kind: "ssh", Name: name, Host: &host, Port: &port, AuthKind: authKind, KeyPath: keyPath, GroupID: groupID, OptionsJSON: options,
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func connectLegacy(t *testing.T, connector *productionConnector, ctx context.Context, row store.AssetRow, database *store.Store) error {
	t.Helper()
	resolved, err := database.AssetGetResolved(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := productionSessionAsset(resolved, false)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := connector.Connect(ctx, asset, 1)
	if transport != nil {
		_ = transport.Close()
	}
	return err
}

func TestMultiUserConnectorRejectsLegacyServerSSHOptions(t *testing.T) {
	database := newKnownHostOwnerTestDB(t)
	marker := filepath.Join(t.TempDir(), "marker")
	password := "password"
	agent := "agent"
	keyPath := "/etc/hostname"
	cases := map[string]store.AssetRow{
		"proxyCommand": legacySSHAsset(t, database, "pc", `{"proxyCommand":"touch `+marker+`"}`, &password, nil, nil),
		"agentSocket":  legacySSHAsset(t, database, "as", `{"agentSocket":"/tmp/x.sock"}`, &password, nil, nil),
		"forwardAgent": legacySSHAsset(t, database, "fa", `{"forwardAgent":true}`, &password, nil, nil),
		"authKind":     legacySSHAsset(t, database, "ak", `{}`, &agent, nil, nil),
		"keyPath":      legacySSHAsset(t, database, "kp", `{}`, &password, &keyPath, nil),
	}
	group, err := database.GroupCreate(t.Context(), store.GroupInput{Name: "g", Defaults: &store.GroupDefaults{AuthKind: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	cases["group default agent"] = legacySSHAsset(t, database, "gd", `{}`, nil, nil, &group.ID)
	keyGroup, err := database.GroupCreate(t.Context(), store.GroupInput{Name: "g2", Defaults: &store.GroupDefaults{AuthKind: "key", KeyPath: keyPath}})
	if err != nil {
		t.Fatal(err)
	}
	cases["group default keyPath"] = legacySSHAsset(t, database, "gk", `{}`, nil, nil, &keyGroup.ID)

	// multiUser flag alone (lost owner in ctx) and owner ctx alone both reject.
	flagged := &productionConnector{database: database, multiUser: true}
	byOwner := &productionConnector{database: database}
	owner := ipc.WithUserID(t.Context(), "user-a")
	for name, row := range cases {
		requireUnsupported(t, connectLegacy(t, flagged, t.Context(), row, database), "flag "+name)
		requireUnsupported(t, connectLegacy(t, byOwner, owner, row, database), "owner "+name)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("proxyCommand was executed")
	}
}

func TestMultiUserConnectorRejectsJumpHopServerOptions(t *testing.T) {
	database := newKnownHostOwnerTestDB(t)
	password := "password"
	jump := legacySSHAsset(t, database, "jump", `{"proxyCommand":"id"}`, &password, nil, nil)
	target := legacySSHAsset(t, database, "target", `{"jumpAssetId":"`+jump.ID+`"}`, &password, nil, nil)
	connector := &productionConnector{database: database, multiUser: true}
	requireUnsupported(t, connectLegacy(t, connector, t.Context(), target, database), "jump hop")
}

func TestMultiUserConnectorRejectsFileBackedPrivateKey(t *testing.T) {
	ctx := t.Context()
	database := newKnownHostOwnerTestDB(t)
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, "test-password"); err != nil {
		t.Fatal(err)
	}
	credID, err := putProductionCredential(ctx, credentialVault, database, "", "k", vault.KindPrivateKey, vault.ReferencedPrivateKey("/etc/hostname", nil).Encode())
	if err != nil {
		t.Fatal(err)
	}
	key := "key"
	host := "127.0.0.1"
	port := int32(1)
	row, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "k", Host: &host, Port: &port, AuthKind: &key, CredID: &credID, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	connector := &productionConnector{database: database, vault: credentialVault, multiUser: true}
	requireUnsupported(t, connectLegacy(t, connector, ctx, row, database), "file credential")
}

func TestMultiUserCredentialCommandsRejectFileSource(t *testing.T) {
	ctx := t.Context()
	database := newKnownHostOwnerTestDB(t)
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, "test-password"); err != nil {
		t.Fatal(err)
	}
	dispatcher := ipc.NewDispatcher()
	if err := registerVaultCommands(dispatcher, credentialVault, database, nil, true); err != nil {
		t.Fatal(err)
	}
	owner := ipc.WithUserID(ctx, "user-a")
	call := func(c context.Context, command, args string) ipc.Response {
		return dispatcher.Dispatch(c, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}
	response := call(owner, "vault_set_credential", `{"args":{"name":"f","kind":"private_key","secret":"/etc/hostname","source":"file"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("file source create = %+v", response)
	}
	response = call(owner, "vault_set_credential", `{"args":{"name":"i","kind":"private_key","secret":"PEM","source":"inline"}}`)
	if !response.OK {
		t.Fatalf("inline create = %+v", response)
	}
	var created map[string]string
	requireStoreTestResponse(t, response, &created)
	response = call(owner, "credential_update", `{"args":{"id":"`+created["id"]+`","secret":"/etc/hostname","source":"file"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("file source update = %+v", response)
	}
	response = call(ctx, "vault_set_credential", `{"args":{"name":"f","kind":"private_key","secret":"/etc/hostname","source":"file"}}`)
	if !response.OK {
		t.Fatalf("desktop file source = %+v", response)
	}
}

func TestDesktopConnectorKeepsServerSSHOptions(t *testing.T) {
	database := newKnownHostOwnerTestDB(t)
	marker := filepath.Join(t.TempDir(), "marker")
	password := "password"
	row := legacySSHAsset(t, database, "pc", `{"proxyCommand":"touch `+marker+`","connectTimeout":1}`, &password, nil, nil)
	connector := &productionConnector{database: database}
	err := connectLegacy(t, connector, t.Context(), row, database)
	var appErr *ipc.Error
	if errors.As(err, &appErr) && appErr.Code == ipc.CodeUnsupported {
		t.Fatalf("desktop connector rejected: %v", err)
	}
}

func TestMultiUserProbeRejectsProxyCommandWithoutJump(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	service := &terminalCommandService{sshConnector: &productionConnector{multiUser: true}}
	err := service.probeSSHReachability(t.Context(), assetProbeTarget{kind: session.KindSSH, options: productionAssetOptions{Host: "127.0.0.1", Port: 1, ProxyCommand: "touch " + marker}}, 0)
	requireUnsupported(t, err, "probe")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("proxyCommand was executed")
	}
	owner := ipc.WithUserID(t.Context(), "user-a")
	err = (&terminalCommandService{}).probeSSHReachability(owner, assetProbeTarget{kind: session.KindSSH, options: productionAssetOptions{Host: "127.0.0.1", Port: 1, ProxyCommand: "touch " + marker}}, 0)
	requireUnsupported(t, err, "probe owner")
}

func TestMultiUserRejectsLocalAssets(t *testing.T) {
	database := newKnownHostOwnerTestDB(t)
	owner := ipc.WithUserID(t.Context(), "user-a")
	_, err := database.AssetCreate(owner, store.AssetInput{Kind: "local", Name: "l", OptionsJSON: "{}"})
	requireUnsupported(t, err, "create local")

	marker := filepath.Join(t.TempDir(), "marker")
	row, err := database.AssetCreate(t.Context(), store.AssetInput{
		Kind: "local", Name: "legacy", OptionsJSON: `{"shell":"/bin/sh","env":{"M":"` + marker + `"}}`,
	})
	if err != nil {
		t.Fatalf("desktop create local: %v", err)
	}
	resolved, err := database.AssetGetLiveResolved(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := productionSessionAsset(resolved, false)
	if err != nil {
		t.Fatal(err)
	}
	for name, connector := range map[string]*productionConnector{"flag": {database: database, multiUser: true}, "owner": {database: database}} {
		ctx := t.Context()
		if name == "owner" {
			ctx = owner
		}
		transport, err := connector.Connect(ctx, asset, 1)
		if transport != nil {
			_ = transport.Close()
		}
		requireUnsupported(t, err, "connect local "+name)
	}
	transport, err := (&productionConnector{database: database}).Connect(t.Context(), asset, 1)
	if err != nil {
		t.Fatalf("desktop local connect: %v", err)
	}
	_ = transport.Close()
}

// createLocalFixtureAsset stores a local asset for an owner the way a desktop sync or legacy row would, since
// the write path refuses local assets for accounts. Tests that only need a cheap session fixture use it.
func createLocalFixtureAsset(ctx context.Context, database *store.Store, input store.AssetInput) (store.AssetRow, error) {
	row, err := database.AssetCreate(context.Background(), input)
	if err != nil {
		return store.AssetRow{}, err
	}
	owner := ipc.OwnerID(ctx)
	if owner == "" {
		return row, nil
	}
	if _, err := database.DB().Exec("UPDATE asset SET owner_id = ? WHERE id = ?", owner, row.ID); err != nil {
		return store.AssetRow{}, err
	}
	return database.AssetGet(ctx, row.ID)
}
