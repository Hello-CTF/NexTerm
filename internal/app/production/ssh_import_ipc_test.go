package production

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/keys"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
	gossh "golang.org/x/crypto/ssh"
)

func sshImportTestRig(t *testing.T) (*ipc.Dispatcher, *store.Store, *vault.Vault) {
	t.Helper()
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	credentialVault := vault.Load(ctx, database)
	dispatcher := ipc.NewDispatcher()
	if err := registerVaultCommands(dispatcher, credentialVault, database); err != nil {
		t.Fatal(err)
	}
	if err := registerSSHImportCommands(dispatcher, database, credentialVault); err != nil {
		t.Fatal(err)
	}
	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_init_master", `{"password":"test-password"}`))
	return dispatcher, database, credentialVault
}

func sshImportTestKeyFile(t *testing.T, dir string) (path, fingerprint string) {
	t.Helper()
	pair, err := keys.Generate(keys.Options{Algorithm: keys.AlgorithmEd25519})
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, pair.PrivateKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, pair.Fingerprint
}

func sshImportTestConfig(t *testing.T, dir, keyPath string) string {
	t.Helper()
	content := "Host bastion\n  HostName bastion.example.com\n  User admin\n\n" +
		"Host web\n  HostName web.example.com\n  User deploy\n  Port 2222\n  IdentityFile " + keyPath + "\n  ProxyJump bastion\n\n" +
		"Host db\n  HostName db.example.com\n  User postgres\n"
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func sshImportPreview(t *testing.T, dispatcher *ipc.Dispatcher, path string) sshImportPreviewDTO {
	t.Helper()
	var preview sshImportPreviewDTO
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "ssh_import_preview",
		`{"args":{"source":"ssh-config","path":`+strconvQuote(path)+`}}`), &preview)
	return preview
}

func sshImportApply(t *testing.T, dispatcher *ipc.Dispatcher, body string) (sshImportApplyDTO, ipc.Response) {
	t.Helper()
	response := dispatchStoreTest(dispatcher, "ssh_import_apply", body)
	var result sshImportApplyDTO
	if response.OK {
		requireStoreTestResponse(t, response, &result)
	}
	return result, response
}

func strconvQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func previewHost(t *testing.T, preview sshImportPreviewDTO, alias string) sshImportHostDTO {
	t.Helper()
	for _, host := range preview.Hosts {
		if host.Alias == alias {
			return host
		}
	}
	t.Fatalf("host %q not in preview %+v", alias, preview.Hosts)
	return sshImportHostDTO{}
}

func previewKey(t *testing.T, preview sshImportPreviewDTO, name string) sshImportKeyDTO {
	t.Helper()
	for _, key := range preview.Keys {
		if len(key.Aliases) > 0 && key.Aliases[0] == name {
			return key
		}
	}
	t.Fatalf("key %q not in preview %+v", name, preview.Keys)
	return sshImportKeyDTO{}
}

func TestSSHImportPreviewToApply(t *testing.T) {
	ctx := t.Context()
	dispatcher, database, _ := sshImportTestRig(t)
	dir := t.TempDir()
	keyPath, keyFingerprint := sshImportTestKeyFile(t, dir)
	configPath := sshImportTestConfig(t, dir, keyPath)

	bastionHost := "bastion.example.com"
	bastionPort := int32(22)
	bastionUser := "admin"
	bastion, err := database.AssetCreate(ctx, store.AssetInput{
		Kind: "ssh", Name: "bastion", Host: &bastionHost, Port: &bastionPort, Username: &bastionUser, OptionsJSON: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}

	preview := sshImportPreview(t, dispatcher, configPath)
	if got := previewHost(t, preview, "bastion").Action; got != "skip-duplicate" {
		t.Fatalf("bastion action = %q, want skip-duplicate", got)
	}
	web := previewHost(t, preview, "web")
	if web.Action != "add" || web.ProxyJump != "bastion" || web.AuthMethod != "key" {
		t.Fatalf("web preview = %+v", web)
	}
	if got := previewKey(t, preview, "id_ed25519"); got.Action != "add" || got.Fingerprint != keyFingerprint {
		t.Fatalf("key preview = %+v", got)
	}
	rawPreview, _ := json.Marshal(preview)
	if strings.Contains(string(rawPreview), "PRIVATE KEY") {
		t.Fatal("preview leaks private key material")
	}

	applyBody := `{"args":{"source":"ssh-config","path":` + strconvQuote(configPath) + `,` +
		`"hosts":[{"alias":"bastion","action":"import"},{"alias":"web","action":"import"},{"alias":"db","action":"import"}],` +
		`"keys":[{"name":"id_ed25519","action":"import"}]}}`
	result, response := sshImportApply(t, dispatcher, applyBody)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsCreated != 2 || result.CredentialsCreated != 1 || result.Skipped != 1 {
		t.Fatalf("apply result = %+v", result)
	}

	assets, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.AssetRow{}
	for _, row := range assets {
		byName[row.Name] = row
	}
	webRow, ok := byName["web"]
	if !ok {
		t.Fatal("web asset not created")
	}
	if webRow.Host == nil || *webRow.Host != "web.example.com" || webRow.Port == nil || *webRow.Port != 2222 {
		t.Fatalf("web endpoint = %+v", webRow)
	}
	if webRow.AuthKind == nil || *webRow.AuthKind != "key" || webRow.CredID == nil {
		t.Fatalf("web auth = %+v", webRow)
	}
	var options map[string]any
	if err := json.Unmarshal(store.ParseJSONOr(webRow.OptionsJSON), &options); err != nil {
		t.Fatal(err)
	}
	if options["jumpAssetId"] != bastion.ID {
		t.Fatalf("web jumpAssetId = %v, want %s", options["jumpAssetId"], bastion.ID)
	}
	if len(assets) != 3 {
		t.Fatalf("asset count = %d, want 3 (bastion pre-existing + web + db)", len(assets))
	}

	credRow, err := database.CredentialGetRow(ctx, *webRow.CredID)
	if err != nil {
		t.Fatal(err)
	}
	if credRow.Name != "id_ed25519" || credRow.Kind != vault.KindPrivateKey {
		t.Fatalf("credential = %+v", credRow)
	}

	auditRows, err := database.AuditQuery(ctx, store.AuditQuery{Source: sshImportStrPtr("user"), Kind: sshImportStrPtr("ssh-import")})
	if err != nil {
		t.Fatal(err)
	}
	if len(auditRows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(auditRows))
	}
	auditPayload := string(auditRows[0].PayloadJSON)
	if !strings.Contains(auditPayload, `"web"`) || !strings.Contains(auditPayload, keyFingerprint) {
		t.Fatalf("audit payload = %s", auditPayload)
	}
	if strings.Contains(auditPayload, "PRIVATE KEY") || strings.Contains(auditPayload, keyPath) {
		t.Fatalf("audit payload leaks secret material: %s", auditPayload)
	}

	second, response := sshImportApply(t, dispatcher, applyBody)
	if !response.OK {
		t.Fatalf("second apply failed: %+v", response.Error)
	}
	if second.AssetsCreated != 0 || second.CredentialsCreated != 0 || second.Skipped != 4 {
		t.Fatalf("second apply = %+v", second)
	}
	rePreview := sshImportPreview(t, dispatcher, configPath)
	if got := previewKey(t, rePreview, "id_ed25519").Action; got != "skip-duplicate" {
		t.Fatalf("key re-preview action = %q, want skip-duplicate (fingerprint dedupe)", got)
	}
}

func TestSSHImportConflictsAndOverwrite(t *testing.T) {
	ctx := t.Context()
	dispatcher, database, _ := sshImportTestRig(t)
	dir := t.TempDir()
	keyPath, _ := sshImportTestKeyFile(t, dir)
	configPath := sshImportTestConfig(t, dir, keyPath)

	oldHost := "old.example.com"
	oldPort := int32(22)
	oldUser := "deploy"
	if _, err := database.AssetCreate(ctx, store.AssetInput{
		Kind: "ssh", Name: "web", Host: &oldHost, Port: &oldPort, Username: &oldUser, OptionsJSON: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	dbHost := "db.example.com"
	dbPort := int32(22)
	dbUser := "postgres"
	if _, err := database.AssetCreate(ctx, store.AssetInput{
		Kind: "ssh", Name: "db-existing", Host: &dbHost, Port: &dbPort, Username: &dbUser, OptionsJSON: "{}",
	}); err != nil {
		t.Fatal(err)
	}

	preview := sshImportPreview(t, dispatcher, configPath)
	if got := previewHost(t, preview, "web").Action; got != "conflict-alias" {
		t.Fatalf("web action = %q, want conflict-alias", got)
	}
	if got := previewHost(t, preview, "db").Action; got != "conflict-endpoint" {
		t.Fatalf("db action = %q, want conflict-endpoint", got)
	}

	body := `{"args":{"source":"ssh-config","path":` + strconvQuote(configPath) + `,` +
		`"hosts":[{"alias":"web","action":"overwrite"},{"alias":"db","action":"overwrite"},{"alias":"bastion","action":"import"}],` +
		`"keys":[]}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsUpdated != 1 || result.AssetsCreated != 1 {
		t.Fatalf("apply result = %+v", result)
	}

	assets, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range assets {
		if row.Name == "web" {
			if row.Host == nil || *row.Host != "web.example.com" || row.Port == nil || *row.Port != 2222 {
				t.Fatalf("overwritten web = %+v", row)
			}
		}
		if row.Name == "db" {
			t.Fatal("endpoint-conflict host must not be imported via overwrite")
		}
	}
}

func TestSSHImportTermiusRequiresConfirmation(t *testing.T) {
	dispatcher, _, _ := sshImportTestRig(t)
	response := dispatchStoreTest(dispatcher, "ssh_import_preview", `{"args":{"source":"termius"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("unconfirmed termius preview = %+v, want bad_param", response)
	}
	if !strings.Contains(response.Error.Message, "确认") {
		t.Fatalf("termius confirmation message = %q", response.Error.Message)
	}
	if runtime.GOOS == "darwin" {
		t.Skip("darwin would touch the real Termius keychain")
	}
	response = dispatchStoreTest(dispatcher, "ssh_import_preview", `{"args":{"source":"termius","confirmed":true,"path":"/nonexistent"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("termius preview on %s = %+v, want unsupported", runtime.GOOS, response)
	}
}

func TestSSHImportApplyRequiresUnlockedVault(t *testing.T) {
	dispatcher, _, _ := sshImportTestRig(t)
	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_lock", `{}`))
	response := dispatchStoreTest(dispatcher, "ssh_import_apply", `{"args":{"source":"ssh-config","path":"/nonexistent","hosts":[],"keys":[]}}`)
	if response.OK || response.Error == nil {
		t.Fatalf("locked apply = %+v, want error", response)
	}
}

func TestVaultGenerateKey(t *testing.T) {
	ctx := t.Context()
	dispatcher, database, credentialVault := sshImportTestRig(t)

	var generated generatedKeyDTO
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "vault_generate_key",
		`{"args":{"name":"deploy-key","algorithm":"ed25519","passphrase":"hunter2"}}`), &generated)
	if generated.ID == "" || generated.Algorithm != "ed25519" || !strings.HasPrefix(generated.Fingerprint, "SHA256:") {
		t.Fatalf("generated = %+v", generated)
	}
	publicKey, _, _, _, err := gossh.ParseAuthorizedKey([]byte(generated.PublicKey))
	if err != nil {
		t.Fatalf("public line does not parse: %v", err)
	}
	if gossh.FingerprintSHA256(publicKey) != generated.Fingerprint {
		t.Fatal("fingerprint does not match authorized_keys line")
	}

	row, err := database.CredentialGetRow(ctx, generated.ID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := credentialVault.DecryptCredentialString(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	payload := vault.ParsePrivateKeyPayload(plaintext)
	if payload.Key == nil || !strings.Contains(*payload.Key, "BEGIN OPENSSH PRIVATE KEY") {
		t.Fatal("stored payload has no private key PEM")
	}
	if payload.Passphrase == nil || *payload.Passphrase != "hunter2" {
		t.Fatalf("stored passphrase = %+v", payload.Passphrase)
	}
	signer, err := gossh.ParsePrivateKeyWithPassphrase([]byte(*payload.Key), []byte("hunter2"))
	if err != nil {
		t.Fatalf("stored key does not open with passphrase: %v", err)
	}
	if gossh.FingerprintSHA256(signer.PublicKey()) != generated.Fingerprint {
		t.Fatal("stored private key does not match public fingerprint")
	}

	response := dispatchStoreTest(dispatcher, "vault_generate_key", `{"args":{"name":"deploy-key"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("duplicate name = %+v, want bad_param", response)
	}

	var rsa generatedKeyDTO
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "vault_generate_key",
		`{"args":{"name":"rsa-key","algorithm":"rsa"}}`), &rsa)
	if rsa.Algorithm != "rsa" || !strings.HasPrefix(rsa.PublicKey, "ssh-rsa ") {
		t.Fatalf("rsa generated = %+v", rsa)
	}

	response = dispatchStoreTest(dispatcher, "vault_generate_key", `{"args":{"name":"bad","algorithm":"dsa"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("bogus algorithm = %+v, want bad_param", response)
	}
}

func sshImportStrPtr(value string) *string { return &value }
