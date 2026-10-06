package production

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/keys"
	"github.com/ProbiusOfficial/NexTerm/internal/sshconfig"
	"github.com/ProbiusOfficial/NexTerm/internal/sshconfig/termiusdb"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
	"golang.org/x/crypto/nacl/secretbox"
	gossh "golang.org/x/crypto/ssh"
)

func sshImportTestRig(t *testing.T) (*ipc.Dispatcher, *store.Store, *vault.Vault, *sshImportPlanner) {
	t.Helper()
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	credentialVault := vault.Load(ctx, database)
	planner := &sshImportPlanner{database: database, vault: credentialVault, termiusKeySource: termiusdb.PlatformKeySource}
	dispatcher := ipc.NewDispatcher()
	if err := registerVaultCommands(dispatcher, credentialVault, database); err != nil {
		t.Fatal(err)
	}
	if err := planner.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_init_master", `{"password":"test-password"}`))
	return dispatcher, database, credentialVault, planner
}

func sshImportTestKeyFile(t *testing.T, dir string) (path, fingerprint string) {
	t.Helper()
	return sshImportWriteKeyFile(t, dir, "id_ed25519", "")
}

func sshImportWriteKeyFile(t *testing.T, dir, name, passphrase string) (path, fingerprint string) {
	t.Helper()
	pair, err := keys.Generate(keys.Options{Algorithm: keys.AlgorithmEd25519, Passphrase: passphrase})
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, name)
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

func sshImportPreview(t *testing.T, dispatcher *ipc.Dispatcher, body string) sshImportPreviewDTO {
	t.Helper()
	var preview sshImportPreviewDTO
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "ssh_import_preview", body), &preview)
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

func assetOptions(t *testing.T, row store.AssetRow) map[string]any {
	t.Helper()
	options := map[string]any{}
	if err := json.Unmarshal(store.ParseJSONOr(row.OptionsJSON), &options); err != nil {
		t.Fatal(err)
	}
	return options
}

func TestSSHImportPreviewToApply(t *testing.T) {
	ctx := t.Context()
	dispatcher, database, _, _ := sshImportTestRig(t)
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

	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	if got := previewHost(t, preview, "bastion").Action; got != "skip-duplicate" {
		t.Fatalf("bastion action = %q, want skip-duplicate", got)
	}
	web := previewHost(t, preview, "web")
	if web.Action != "add" || web.ProxyJump != "bastion" || web.AuthMethod != "key" {
		t.Fatalf("web preview = %+v", web)
	}
	if web.ID == "" || previewKey(t, preview, "id_ed25519").ID == "" {
		t.Fatal("preview items missing stable ids")
	}
	if got := previewKey(t, preview, "id_ed25519"); got.Action != "add" || got.Fingerprint != keyFingerprint {
		t.Fatalf("key preview = %+v", got)
	}
	rawPreview, _ := json.Marshal(preview)
	if strings.Contains(string(rawPreview), "PRIVATE KEY") {
		t.Fatal("preview leaks private key material")
	}

	bastionIdentity := sshImportHostIdentity(&sshconfig.HostPreview{Alias: "bastion", Username: "admin", Hostname: "bastion.example.com", Port: 22}, 0)
	webIdentity := sshImportHostIdentity(&sshconfig.HostPreview{Alias: "web", Username: "deploy", Hostname: "web.example.com", Port: 2222}, 0)
	dbIdentity := sshImportHostIdentity(&sshconfig.HostPreview{Alias: "db", Username: "postgres", Hostname: "db.example.com", Port: 22}, 0)
	keyIdentity := sshImportKeyIdentity(&sshconfig.KeyPreview{Aliases: []string{"id_ed25519"}, Fingerprint: keyFingerprint, Path: keyPath}, 0)
	applyBody := `{"args":{"source":"ssh-config","path":` + strconvQuote(configPath) + `,` +
		`"hosts":[{"id":` + strconvQuote(bastionIdentity) + `,"action":"import"},{"id":` + strconvQuote(webIdentity) + `,"action":"import"},{"id":` + strconvQuote(dbIdentity) + `,"action":"import"}],` +
		`"keys":[{"id":` + strconvQuote(keyIdentity) + `,"action":"import"}]}}`
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
	if webRow.KeyPath != nil {
		t.Fatalf("bound web asset must not set keyPath (credential drives auth), got %q", *webRow.KeyPath)
	}
	options := assetOptions(t, webRow)
	if options["jumpAssetId"] != bastion.ID {
		t.Fatalf("web jumpAssetId = %v, want %s", options["jumpAssetId"], bastion.ID)
	}
	dbRow, ok := byName["db"]
	if !ok {
		t.Fatal("db asset not created")
	}
	if dbRow.AuthKind == nil || *dbRow.AuthKind != "agent" {
		t.Fatalf("db authKind = %+v, want agent", dbRow.AuthKind)
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
	rePreview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	if got := previewKey(t, rePreview, "id_ed25519").Action; got != "skip-duplicate" {
		t.Fatalf("key re-preview action = %q, want skip-duplicate (fingerprint dedupe)", got)
	}
}

func TestSSHImportConflictsOverwriteAndMultiHop(t *testing.T) {
	ctx := t.Context()
	dispatcher, database, _, _ := sshImportTestRig(t)
	dir := t.TempDir()
	content := "Host bastion\n  HostName bastion.example.com\n  User admin\n\n" +
		"Host web\n  HostName web.example.com\n  User deploy\n  Port 2222\n  ProxyJump bastion\n\n" +
		"Host legacy\n  HostName legacy-new.example.com\n  User root\n\n" +
		"Host deep\n  HostName deep.example.com\n  User root\n  ProxyJump bastion,web\n\n" +
		"Host db\n  HostName db.example.com\n  User postgres\n"
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	oldHost := "old.example.com"
	oldPort := int32(22)
	oldUser := "deploy"
	web, err := database.AssetCreate(ctx, store.AssetInput{
		Kind: "ssh", Name: "web", Host: &oldHost, Port: &oldPort, Username: &oldUser,
		OptionsJSON: `{"jumpAssetId":"stale-jump","connectTimeout":30}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyHost := "legacy-old.example.com"
	if _, err := database.AssetCreate(ctx, store.AssetInput{
		Kind: "ssh", Name: "legacy", Host: &legacyHost, Port: &oldPort, Username: &oldUser,
		OptionsJSON: `{"jumpAssetId":"stale-jump","initialCommand":"tmux attach"}`,
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

	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	if got := previewHost(t, preview, "web").Action; got != "conflict-alias" {
		t.Fatalf("web action = %q, want conflict-alias", got)
	}
	if got := previewHost(t, preview, "db").Action; got != "conflict-endpoint" {
		t.Fatalf("db action = %q, want conflict-endpoint", got)
	}
	deep := previewHost(t, preview, "deep")
	if deep.Action != "blocked-jump" {
		t.Fatalf("deep action = %q, want blocked-jump (multi-hop)", deep.Action)
	}
	if len(deep.Warnings) == 0 || !strings.Contains(strings.Join(deep.Warnings, " "), "多跳") {
		t.Fatalf("deep warnings = %+v", deep.Warnings)
	}

	bastionID := previewHost(t, preview, "bastion").ID
	webID := previewHost(t, preview, "web").ID
	legacyID := previewHost(t, preview, "legacy").ID
	deepID := deep.ID
	body := `{"args":{"source":"ssh-config","path":` + strconvQuote(configPath) + `,` +
		`"hosts":[{"id":"` + bastionID + `","action":"import"},{"id":"` + webID + `","action":"overwrite"},{"id":"` + legacyID + `","action":"overwrite"},{"id":"` + deepID + `","action":"import"}],` +
		`"keys":[]}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsUpdated != 2 || result.AssetsCreated != 1 || result.Skipped != 1 {
		t.Fatalf("apply result = %+v", result)
	}

	webRow, err := database.AssetGet(ctx, web.ID)
	if err != nil {
		t.Fatal(err)
	}
	if webRow.Host == nil || *webRow.Host != "web.example.com" || webRow.Port == nil || *webRow.Port != 2222 {
		t.Fatalf("overwritten web = %+v", webRow)
	}
	if webRow.AuthKind == nil || *webRow.AuthKind != "agent" {
		t.Fatalf("web authKind = %+v, want agent", webRow.AuthKind)
	}
	webOptions := assetOptions(t, webRow)
	if webOptions["connectTimeout"] != float64(30) {
		t.Fatalf("web options lost non-jump keys: %v", webOptions)
	}
	bastionRow, err := database.AssetGet(ctx, sshImportAssetIDByName(t, database, "bastion"))
	if err != nil {
		t.Fatal(err)
	}
	if webOptions["jumpAssetId"] != bastionRow.ID {
		t.Fatalf("web jumpAssetId = %v, want new bastion %s (stale must be replaced)", webOptions["jumpAssetId"], bastionRow.ID)
	}

	legacyRow, err := database.AssetGet(ctx, sshImportAssetIDByName(t, database, "legacy"))
	if err != nil {
		t.Fatal(err)
	}
	legacyOptions := assetOptions(t, legacyRow)
	if _, ok := legacyOptions["jumpAssetId"]; ok {
		t.Fatalf("legacy jumpAssetId must be removed when new config has no ProxyJump: %v", legacyOptions)
	}
	if legacyOptions["initialCommand"] != "tmux attach" {
		t.Fatalf("legacy options lost non-jump keys: %v", legacyOptions)
	}

	assets, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range assets {
		if row.Name == "db" {
			t.Fatal("endpoint-conflict host must not be imported via overwrite")
		}
		if row.Name == "deep" {
			t.Fatal("multi-hop host must be blocked at preview, not imported")
		}
	}
}

func sshImportAssetIDByName(t *testing.T, database *store.Store, name string) string {
	t.Helper()
	rows, err := database.AssetList(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == name {
			return row.ID
		}
	}
	t.Fatalf("asset %q not found", name)
	return ""
}

func TestSSHImportConflictKeySkippedUnbinds(t *testing.T) {
	ctx := t.Context()
	dispatcher, database, _, _ := sshImportTestRig(t)
	dir := t.TempDir()

	existingPEM, err := keys.Generate(keys.Options{Algorithm: keys.AlgorithmEd25519})
	if err != nil {
		t.Fatal(err)
	}
	var existingID map[string]string
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "vault_set_credential",
		`{"args":{"name":"id_rsa","kind":"private_key","secret":`+strconvQuote(string(existingPEM.PrivateKeyPEM))+`,"source":"inline"}}`), &existingID)

	keyPath, _ := sshImportWriteKeyFile(t, dir, "id_rsa", "")
	content := "Host app\n  HostName app.example.com\n  User deploy\n  IdentityFile " + keyPath + "\n"
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	key := previewKey(t, preview, "id_rsa")
	if key.Action != "conflict-alias" {
		t.Fatalf("key action = %q, want conflict-alias (same name, different fingerprint)", key.Action)
	}
	hostID := previewHost(t, preview, "app").ID

	body := `{"args":{"source":"ssh-config","path":` + strconvQuote(configPath) + `,` +
		`"hosts":[{"id":"` + hostID + `","action":"import"}],` +
		`"keys":[{"id":"` + key.ID + `","action":"skip"}]}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsCreated != 1 || result.Skipped != 1 {
		t.Fatalf("apply result = %+v", result)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, " "), "未入库") {
		t.Fatalf("apply warnings = %+v", result.Warnings)
	}

	rows, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var appRow store.AssetRow
	for _, row := range rows {
		if row.Name == "app" {
			appRow = row
		}
	}
	if appRow.AuthKind == nil || *appRow.AuthKind != "key" {
		t.Fatalf("app authKind = %+v, want key", appRow.AuthKind)
	}
	if appRow.CredID != nil {
		t.Fatalf("app must stay unbound when the conflicting key is skipped, got credId %q (existing %q)", *appRow.CredID, existingID["id"])
	}
	if appRow.KeyPath == nil || *appRow.KeyPath != keyPath {
		t.Fatalf("app keyPath = %+v, want identity file fallback %q", appRow.KeyPath, keyPath)
	}

	credRows, err := database.CredentialList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(credRows) != 1 || credRows[0].ID != existingID["id"] {
		t.Fatalf("credentials changed: %+v", credRows)
	}
}

func TestSSHImportPassphraseKeyDedupe(t *testing.T) {
	ctx := t.Context()
	dispatcher, database, _, _ := sshImportTestRig(t)
	dir := t.TempDir()

	_, rawSigner, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	plainBlock, err := gossh.MarshalPrivateKey(rawSigner, "nexterm")
	if err != nil {
		t.Fatal(err)
	}
	encryptedBlock, err := gossh.MarshalPrivateKeyWithPassphrase(rawSigner, "nexterm", []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	encryptedPEM := string(pem.EncodeToMemory(encryptedBlock))

	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(plainBlock), 0o600); err != nil {
		t.Fatal(err)
	}

	var inlineID map[string]string
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "vault_set_credential",
		`{"args":{"name":"pp-key","kind":"private_key","secret":`+strconvQuote(encryptedPEM)+`,"source":"inline","passphrase":"pw"}}`), &inlineID)

	content := "Host app\n  HostName app.example.com\n  User deploy\n  IdentityFile " + keyPath + "\n"
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	key := previewKey(t, preview, "id_ed25519")
	if key.Action != "skip-duplicate" {
		t.Fatalf("passphrase-protected key action = %q, want skip-duplicate (fingerprint via stored passphrase)", key.Action)
	}

	refPath := filepath.Join(dir, "ref_key")
	if err := os.WriteFile(refPath, []byte(encryptedPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	var refID map[string]string
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "vault_set_credential",
		`{"args":{"name":"pp-ref","kind":"private_key","secret":`+strconvQuote(refPath)+`,"source":"file","passphrase":"pw"}}`), &refID)
	rePreview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	if got := previewKey(t, rePreview, "id_ed25519").Action; got != "skip-duplicate" {
		t.Fatalf("key action with passphrase ref = %q, want skip-duplicate", got)
	}

	hostID := previewHost(t, rePreview, "app").ID
	body := `{"args":{"source":"ssh-config","path":` + strconvQuote(configPath) + `,` +
		`"hosts":[{"id":"` + hostID + `","action":"import"}],` +
		`"keys":[]}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsCreated != 1 {
		t.Fatalf("apply result = %+v", result)
	}
	rows, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, assetRow := range rows {
		if assetRow.Name == "app" {
			if assetRow.CredID == nil || (*assetRow.CredID != inlineID["id"] && *assetRow.CredID != refID["id"]) {
				t.Fatalf("app credId = %+v, want one of the passphrase credentials", assetRow.CredID)
			}
			if assetRow.AuthKind == nil || *assetRow.AuthKind != "key" {
				t.Fatalf("app authKind = %+v", assetRow.AuthKind)
			}
		}
	}
}

func sshImportTermiusFixture(t *testing.T, key []byte, records []string) string {
	t.Helper()
	dir := t.TempDir()
	var sb strings.Builder
	for _, record := range records {
		var nonce [24]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			t.Fatal(err)
		}
		box := secretbox.Seal(nil, []byte(record), &nonce, (*[32]byte)(key))
		raw := append([]byte{4, 0}, nonce[:]...)
		raw = append(raw, box...)
		sb.WriteString(base64.StdEncoding.EncodeToString(raw))
		sb.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "000003.log"), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func sshImportTermiusRig(t *testing.T, key []byte) (*ipc.Dispatcher, *store.Store, *vault.Vault) {
	t.Helper()
	dispatcher, database, credentialVault, planner := sshImportTestRig(t)
	planner.termiusKeySource = func() (termiusdb.KeySource, error) {
		return func() ([]byte, error) { return key, nil }, nil
	}
	return dispatcher, database, credentialVault
}

func sshImportTermiusBody(dbPath string, extra string) string {
	return `{"args":{"source":"termius","confirmed":true,"path":` + strconvQuote(dbPath) + extra + `}}`
}

func TestSSHImportTermiusAgentAndSameNameKeys(t *testing.T) {
	ctx := t.Context()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	pemA, fpA := sshImportTermiusPEM(t)
	pemB, _ := sshImportTermiusPEM(t)
	dbPath := sshImportTermiusFixture(t, key, []string{
		`{"private_key": ` + strconvQuote(pemA) + `, "label": "shared", "id": "k1"}`,
		`{"private_key": ` + strconvQuote(pemB) + `, "label": "shared", "id": "k2"}`,
		`{"host": "agent.example.com", "user_name": "root", "port": 22, "title": "agent-host"}`,
		`{"host": "keyed.example.com", "user_name": "deploy", "port": 22, "title": "key-host", "key_id": "k1"}`,
	})
	dispatcher, database, credentialVault := sshImportTermiusRig(t, key)

	preview := sshImportPreview(t, dispatcher, sshImportTermiusBody(dbPath, ""))
	agentHost := previewHost(t, preview, "agent-host")
	if agentHost.Action != "add" || agentHost.AuthMethod != "agent" {
		t.Fatalf("agent-host = %+v", agentHost)
	}
	keyHost := previewHost(t, preview, "key-host")
	if keyHost.KeyName != "shared" || keyHost.AuthMethod != "key" {
		t.Fatalf("key-host = %+v", keyHost)
	}
	if len(preview.Keys) != 2 || preview.Keys[0].Action != "add" || preview.Keys[1].Action != "conflict-alias" {
		t.Fatalf("same-name keys = %+v", preview.Keys)
	}
	if preview.Keys[0].ID == preview.Keys[1].ID || preview.Hosts[0].ID == preview.Hosts[1].ID {
		t.Fatal("same-name items must have distinct stable ids")
	}

	body := `{"args":{"source":"termius","confirmed":true,"path":` + strconvQuote(dbPath) + `,` +
		`"hosts":[{"id":"` + agentHost.ID + `","action":"import"},{"id":"` + keyHost.ID + `","action":"import"}],` +
		`"keys":[{"id":"` + preview.Keys[0].ID + `","action":"import"},{"id":"` + preview.Keys[1].ID + `","action":"skip"}]}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsCreated != 2 || result.CredentialsCreated != 1 {
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
	agentRow := byName["agent-host"]
	if agentRow.AuthKind == nil || *agentRow.AuthKind != "agent" {
		t.Fatalf("agent-host authKind = %+v, want agent", agentRow.AuthKind)
	}
	keyRow := byName["key-host"]
	if keyRow.AuthKind == nil || *keyRow.AuthKind != "key" || keyRow.CredID == nil {
		t.Fatalf("key-host auth = %+v", keyRow)
	}
	credRow, err := database.CredentialGetRow(ctx, *keyRow.CredID)
	if err != nil {
		t.Fatal(err)
	}
	if credRow.Name != "shared" {
		t.Fatalf("bound credential = %+v", credRow)
	}
	plaintext, err := credentialVault.DecryptCredentialString(ctx, credRow)
	if err != nil {
		t.Fatal(err)
	}
	stored := vault.ParsePrivateKeyPayload(plaintext)
	if stored.Key == nil || *stored.Key != pemA {
		t.Fatal("bound credential does not hold the canonical first key material")
	}
	signer, err := gossh.ParsePrivateKey([]byte(*stored.Key))
	if err != nil {
		t.Fatal(err)
	}
	if gossh.FingerprintSHA256(signer.PublicKey()) != fpA {
		t.Fatal("bound credential fingerprint mismatch")
	}
}

func TestSSHImportTermiusConflictKeySkippedUnbinds(t *testing.T) {
	ctx := t.Context()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	existingPEM, err := keys.Generate(keys.Options{Algorithm: keys.AlgorithmEd25519})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, database, _ := sshImportTermiusRig(t, key)
	var existingID map[string]string
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "vault_set_credential",
		`{"args":{"name":"shared","kind":"private_key","secret":`+strconvQuote(string(existingPEM.PrivateKeyPEM))+`,"source":"inline"}}`), &existingID)

	pemNew, _ := sshImportTermiusPEM(t)
	dbPath := sshImportTermiusFixture(t, key, []string{
		`{"private_key": ` + strconvQuote(pemNew) + `, "label": "shared", "id": "k1"}`,
		`{"host": "keyed.example.com", "user_name": "deploy", "port": 22, "title": "key-host", "key_id": "k1"}`,
	})

	preview := sshImportPreview(t, dispatcher, sshImportTermiusBody(dbPath, ""))
	keyItem := previewKey(t, preview, "shared")
	if keyItem.Action != "conflict-alias" {
		t.Fatalf("key action = %q, want conflict-alias", keyItem.Action)
	}
	hostID := previewHost(t, preview, "key-host").ID

	body := `{"args":{"source":"termius","confirmed":true,"path":` + strconvQuote(dbPath) + `,` +
		`"hosts":[{"id":"` + hostID + `","action":"import"}],` +
		`"keys":[{"id":"` + keyItem.ID + `","action":"skip"}]}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsCreated != 1 || result.Skipped != 1 {
		t.Fatalf("apply result = %+v", result)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, " "), "保持未绑定") {
		t.Fatalf("apply warnings = %+v", result.Warnings)
	}

	assets, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range assets {
		if row.Name == "key-host" {
			if row.CredID != nil {
				t.Fatalf("key-host must stay unbound, got credId %q (existing %q)", *row.CredID, existingID["id"])
			}
			if row.AuthKind == nil || *row.AuthKind != "key" {
				t.Fatalf("key-host authKind = %+v, want key", row.AuthKind)
			}
		}
	}
	credRows, err := database.CredentialList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(credRows) != 1 || credRows[0].ID != existingID["id"] {
		t.Fatalf("existing credential must be untouched: %+v", credRows)
	}
}

func sshImportTermiusPEM(t *testing.T) (string, string) {
	t.Helper()
	pair, err := keys.Generate(keys.Options{Algorithm: keys.AlgorithmEd25519})
	if err != nil {
		t.Fatal(err)
	}
	return string(pair.PrivateKeyPEM), pair.Fingerprint
}

func TestSSHImportSelectionIdentitySurvivesChanges(t *testing.T) {
	ctx := t.Context()
	dispatcher, database, _, _ := sshImportTestRig(t)
	dir := t.TempDir()

	configV1 := "Host bastion\n  HostName bastion.example.com\n  User admin\n\n" +
		"Host web\n  HostName web.example.com\n  User deploy\n"
	pathV1 := filepath.Join(dir, "config-v1")
	if err := os.WriteFile(pathV1, []byte(configV1), 0o600); err != nil {
		t.Fatal(err)
	}
	configV2 := "Host aaa\n  HostName aaa.example.com\n  User root\n\n" + configV1
	pathV2 := filepath.Join(dir, "config-v2")
	if err := os.WriteFile(pathV2, []byte(configV2), 0o600); err != nil {
		t.Fatal(err)
	}

	previewV1 := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(pathV1)+`}}`)
	if len(previewV1.Hosts) != 2 {
		t.Fatalf("v1 hosts = %+v", previewV1.Hosts)
	}
	bastionID := previewHost(t, previewV1, "bastion").ID
	webID := previewHost(t, previewV1, "web").ID

	body := `{"args":{"source":"ssh-config","path":` + strconvQuote(pathV2) + `,` +
		`"hosts":[{"id":` + strconvQuote(bastionID) + `,"action":"import"},{"id":` + strconvQuote(webID) + `,"action":"import"},{"id":"ghost|root@ghost.example.com:22#0","action":"import"}],` +
		`"keys":[]}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsCreated != 2 || result.Skipped != 1 {
		t.Fatalf("apply result = %+v", result)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, " "), "不一致") {
		t.Fatalf("stale selection must warn: %+v", result.Warnings)
	}

	assets, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, row := range assets {
		if row.Host != nil {
			byName[row.Name] = *row.Host
		}
	}
	if byName["bastion"] != "bastion.example.com" || byName["web"] != "web.example.com" {
		t.Fatalf("imported hosts = %v", byName)
	}
	if _, ok := byName["aaa"]; ok {
		t.Fatal("host inserted after preview must not be imported by a stale selection")
	}
}

func TestSSHImportCaseVariantJumpNotOverwritten(t *testing.T) {
	ctx := t.Context()
	dispatcher, database, _, _ := sshImportTestRig(t)
	dir := t.TempDir()
	content := "Host bastion\n  HostName bastion.example.com\n  User admin\n\n" +
		"Host db\n  HostName db.example.com\n  User postgres\n\n" +
		"Host Dup\n  HostName dup1.example.com\n  User root\n  ProxyJump bastion\n\n" +
		"Host dup\n  HostName dup2.example.com\n  User root\n  ProxyJump db\n"
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	dupUpper := previewHost(t, preview, "Dup")
	dupLower := previewHost(t, preview, "dup")
	if dupUpper.Action != "add" || dupLower.Action != "conflict-alias" {
		t.Fatalf("case-variant actions = %q / %q", dupUpper.Action, dupLower.Action)
	}

	body := `{"args":{"source":"ssh-config","path":` + strconvQuote(configPath) + `,` +
		`"hosts":[{"id":` + strconvQuote(previewHost(t, preview, "bastion").ID) + `,"action":"import"},` +
		`{"id":` + strconvQuote(previewHost(t, preview, "db").ID) + `,"action":"import"},` +
		`{"id":` + strconvQuote(dupUpper.ID) + `,"action":"import"},` +
		`{"id":` + strconvQuote(dupLower.ID) + `,"action":"skip"}],` +
		`"keys":[]}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsCreated != 3 || result.Skipped != 1 {
		t.Fatalf("apply result = %+v", result)
	}

	assets, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	idsByName := map[string]string{}
	for _, row := range assets {
		idsByName[row.Name] = row.ID
	}
	var dupRow store.AssetRow
	for _, row := range assets {
		if row.Name == "Dup" {
			dupRow = row
		}
		if row.Name == "dup" {
			t.Fatal("skipped case-variant row must not create an asset")
		}
	}
	options := assetOptions(t, dupRow)
	if options["jumpAssetId"] != idsByName["bastion"] {
		t.Fatalf("Dup jumpAssetId = %v, want bastion %s (skipped dup row must not overwrite it)", options["jumpAssetId"], idsByName["bastion"])
	}
}

func TestSSHImportTermiusSecondSameNameKeyReferenced(t *testing.T) {
	ctx := t.Context()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	pemA, fpA := sshImportTermiusPEM(t)
	pemB, fpB := sshImportTermiusPEM(t)
	dbPath := sshImportTermiusFixture(t, key, []string{
		`{"private_key": ` + strconvQuote(pemA) + `, "label": "shared", "id": "k1"}`,
		`{"private_key": ` + strconvQuote(pemB) + `, "label": "shared", "id": "k2"}`,
		`{"host": "one.example.com", "user_name": "root", "port": 22, "title": "host-1", "key_id": "k1"}`,
		`{"host": "two.example.com", "user_name": "root", "port": 22, "title": "host-2", "key_id": "k2"}`,
	})
	dispatcher, database, credentialVault := sshImportTermiusRig(t, key)

	preview := sshImportPreview(t, dispatcher, sshImportTermiusBody(dbPath, ""))
	host1 := previewHost(t, preview, "host-1")
	host2 := previewHost(t, preview, "host-2")
	if host1.KeyFingerprint != fpA || host2.KeyFingerprint != fpB {
		t.Fatalf("key references lost: %q / %q, want %q / %q", host1.KeyFingerprint, host2.KeyFingerprint, fpA, fpB)
	}
	key1 := preview.Keys[0]
	key2 := preview.Keys[1]
	if key1.Action != "add" || key2.Action != "conflict-alias" {
		t.Fatalf("key actions = %q / %q", key1.Action, key2.Action)
	}

	body := `{"args":{"source":"termius","confirmed":true,"path":` + strconvQuote(dbPath) + `,` +
		`"hosts":[{"id":` + strconvQuote(host1.ID) + `,"action":"import"},{"id":` + strconvQuote(host2.ID) + `,"action":"import"}],` +
		`"keys":[{"id":` + strconvQuote(key1.ID) + `,"action":"import"},{"id":` + strconvQuote(key2.ID) + `,"action":"skip"}]}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsCreated != 2 || result.CredentialsCreated != 1 {
		t.Fatalf("apply result = %+v", result)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, " "), "保持未绑定") {
		t.Fatalf("skipped k2 must leave host-2 unbound with warning: %+v", result.Warnings)
	}

	assets, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.AssetRow{}
	for _, row := range assets {
		byName[row.Name] = row
	}
	host1Row := byName["host-1"]
	if host1Row.CredID == nil {
		t.Fatal("host-1 must bind the imported k1 credential")
	}
	host2Row := byName["host-2"]
	if host2Row.CredID != nil {
		t.Fatalf("host-2 references k2 (skipped, different fingerprint) and must stay unbound, got credId %q", *host2Row.CredID)
	}
	if host2Row.AuthKind == nil || *host2Row.AuthKind != "key" {
		t.Fatalf("host-2 authKind = %+v, want key", host2Row.AuthKind)
	}
	credRow, err := database.CredentialGetRow(ctx, *host1Row.CredID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := credentialVault.DecryptCredentialString(ctx, credRow)
	if err != nil {
		t.Fatal(err)
	}
	stored := vault.ParsePrivateKeyPayload(plaintext)
	if stored.Key == nil || *stored.Key != pemA {
		t.Fatal("host-1 credential must hold k1 material, not the same-name k2")
	}
}

func TestSSHImportDuplicateOverwriteSameCredential(t *testing.T) {
	ctx := t.Context()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	existingPEM, err := keys.Generate(keys.Options{Algorithm: keys.AlgorithmEd25519})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, database, credentialVault := sshImportTermiusRig(t, key)
	var existingID map[string]string
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "vault_set_credential",
		`{"args":{"name":"shared","kind":"private_key","secret":`+strconvQuote(string(existingPEM.PrivateKeyPEM))+`,"source":"inline"}}`), &existingID)

	pemA, _ := sshImportTermiusPEM(t)
	pemB, _ := sshImportTermiusPEM(t)
	dbPath := sshImportTermiusFixture(t, key, []string{
		`{"private_key": ` + strconvQuote(pemA) + `, "label": "shared", "id": "k1"}`,
		`{"private_key": ` + strconvQuote(pemB) + `, "label": "shared", "id": "k2"}`,
		`{"host": "one.example.com", "user_name": "root", "port": 22, "title": "host-1", "key_id": "k1"}`,
		`{"host": "two.example.com", "user_name": "root", "port": 22, "title": "host-2", "key_id": "k2"}`,
	})

	preview := sshImportPreview(t, dispatcher, sshImportTermiusBody(dbPath, ""))
	key1 := preview.Keys[0]
	key2 := preview.Keys[1]
	if key1.Action != "conflict-alias" || key2.Action != "conflict-alias" {
		t.Fatalf("key actions = %q / %q, want conflict-alias", key1.Action, key2.Action)
	}
	host1 := previewHost(t, preview, "host-1")
	host2 := previewHost(t, preview, "host-2")

	body := `{"args":{"source":"termius","confirmed":true,"path":` + strconvQuote(dbPath) + `,` +
		`"hosts":[{"id":` + strconvQuote(host1.ID) + `,"action":"import"},{"id":` + strconvQuote(host2.ID) + `,"action":"import"}],` +
		`"keys":[{"id":` + strconvQuote(key1.ID) + `,"action":"overwrite"},{"id":` + strconvQuote(key2.ID) + `,"action":"overwrite"}]}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.CredentialsUpdated != 1 || result.Skipped != 1 || result.AssetsCreated != 2 {
		t.Fatalf("apply result = %+v", result)
	}
	joined := strings.Join(result.Warnings, " ")
	if !strings.Contains(joined, "已在本次导入中被覆盖") {
		t.Fatalf("duplicate overwrite must warn: %+v", result.Warnings)
	}
	if !strings.Contains(joined, "保持未绑定") {
		t.Fatalf("host-2 must stay unbound with warning: %+v", result.Warnings)
	}

	assets, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.AssetRow{}
	for _, row := range assets {
		byName[row.Name] = row
	}
	host1Row := byName["host-1"]
	if host1Row.CredID == nil || *host1Row.CredID != existingID["id"] {
		t.Fatalf("host-1 credId = %+v, want overwritten credential %q", host1Row.CredID, existingID["id"])
	}
	if byName["host-2"].CredID != nil {
		t.Fatal("host-2 must stay unbound (k2 overwrite was skipped)")
	}
	credRow, err := database.CredentialGetRow(ctx, existingID["id"])
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := credentialVault.DecryptCredentialString(ctx, credRow)
	if err != nil {
		t.Fatal(err)
	}
	stored := vault.ParsePrivateKeyPayload(plaintext)
	if stored.Key == nil || *stored.Key != pemA {
		t.Fatal("credential must hold the first overwrite material (k1), not k2")
	}
}

func TestSSHImportOverwriteInvalidatesSkipDuplicate(t *testing.T) {
	ctx := t.Context()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	pemA, _ := sshImportTermiusPEM(t)
	pemB, fpB := sshImportTermiusPEM(t)
	dispatcher, database, credentialVault := sshImportTermiusRig(t, key)
	var existingID map[string]string
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "vault_set_credential",
		`{"args":{"name":"shared","kind":"private_key","secret":`+strconvQuote(pemB)+`,"source":"inline"}}`), &existingID)

	dbPath := sshImportTermiusFixture(t, key, []string{
		`{"private_key": ` + strconvQuote(pemA) + `, "label": "shared", "id": "k1"}`,
		`{"private_key": ` + strconvQuote(pemB) + `, "label": "shared", "id": "k2"}`,
		`{"host": "one.example.com", "user_name": "root", "port": 22, "title": "host-1", "key_id": "k1"}`,
		`{"host": "two.example.com", "user_name": "root", "port": 22, "title": "host-2", "key_id": "k2"}`,
	})

	preview := sshImportPreview(t, dispatcher, sshImportTermiusBody(dbPath, ""))
	key1 := preview.Keys[0]
	key2 := preview.Keys[1]
	if key1.Action != "conflict-alias" || key2.Action != "skip-duplicate" {
		t.Fatalf("key actions = %q / %q, want conflict-alias / skip-duplicate", key1.Action, key2.Action)
	}
	host1 := previewHost(t, preview, "host-1")
	host2 := previewHost(t, preview, "host-2")
	if host2.KeyFingerprint != fpB {
		t.Fatalf("host-2 KeyFingerprint = %q, want %q", host2.KeyFingerprint, fpB)
	}

	body := `{"args":{"source":"termius","confirmed":true,"path":` + strconvQuote(dbPath) + `,` +
		`"hosts":[{"id":` + strconvQuote(host1.ID) + `,"action":"import"},{"id":` + strconvQuote(host2.ID) + `,"action":"import"}],` +
		`"keys":[{"id":` + strconvQuote(key1.ID) + `,"action":"overwrite"},{"id":` + strconvQuote(key2.ID) + `,"action":"skip"}]}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.CredentialsUpdated != 1 || result.AssetsCreated != 2 {
		t.Fatalf("apply result = %+v", result)
	}
	if !strings.Contains(strings.Join(result.Warnings, " "), "保持未绑定") {
		t.Fatalf("host-2 must stay unbound after its fingerprint mapping was invalidated: %+v", result.Warnings)
	}

	assets, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.AssetRow{}
	for _, row := range assets {
		byName[row.Name] = row
	}
	if byName["host-1"].CredID == nil || *byName["host-1"].CredID != existingID["id"] {
		t.Fatalf("host-1 credId = %+v, want overwritten credential", byName["host-1"].CredID)
	}
	if byName["host-2"].CredID != nil {
		t.Fatal("host-2 must not bind the credential whose material was replaced (stale fingerprint index)")
	}
	credRow, err := database.CredentialGetRow(ctx, existingID["id"])
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := credentialVault.DecryptCredentialString(ctx, credRow)
	if err != nil {
		t.Fatal(err)
	}
	stored := vault.ParsePrivateKeyPayload(plaintext)
	if stored.Key == nil || *stored.Key != pemA {
		t.Fatal("credential must hold the overwritten material (k1)")
	}
}

func TestSSHImportTermiusRequiresConfirmation(t *testing.T) {
	dispatcher, _, _, _ := sshImportTestRig(t)
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
	dispatcher, _, _, _ := sshImportTestRig(t)
	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_lock", `{}`))
	response := dispatchStoreTest(dispatcher, "ssh_import_apply", `{"args":{"source":"ssh-config","path":"/nonexistent","hosts":[],"keys":[]}}`)
	if response.OK || response.Error == nil {
		t.Fatalf("locked apply = %+v, want error", response)
	}
}

func TestVaultGenerateKey(t *testing.T) {
	ctx := t.Context()
	dispatcher, database, credentialVault, _ := sshImportTestRig(t)

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
