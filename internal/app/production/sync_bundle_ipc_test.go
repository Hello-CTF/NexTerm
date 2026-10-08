package production

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/server"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	syncservice "github.com/ProbiusOfficial/NexTerm/internal/sync"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func newSyncBundleTestService(t *testing.T, desktop bool, initVault bool) (*store.Store, *vault.Vault, *syncservice.Service) {
	t.Helper()
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	credentialVault := vault.Load(ctx, database)
	if initVault {
		if err := credentialVault.InitMaster(ctx, "sync-bundle-test-master"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(credentialVault.Lock)
	}
	syncService := syncservice.New(database, credentialVault, syncservice.WithMetadata("test-version", desktop))
	return database, credentialVault, syncService
}

func registerSyncBundleDispatcher(t *testing.T, syncService *syncservice.Service) *ipc.Dispatcher {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := syncService.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func dispatchSyncBundle(t *testing.T, dispatcher *ipc.Dispatcher, command string, args any) ipc.Response {
	t.Helper()
	envelope, err := json.Marshal(map[string]any{"args": args})
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: envelope}, ipc.Environment{})
}

func decodeSyncBundleResponse[T any](t *testing.T, response ipc.Response) T {
	t.Helper()
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response.Error)
	}
	var decoded T
	if err := json.Unmarshal(response.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func seedSyncBundleAsset(t *testing.T, database *store.Store, row store.AssetRow) {
	t.Helper()
	if _, err := database.AssetUpsert(t.Context(), row); err != nil {
		t.Fatal(err)
	}
}

func TestSyncBundleDigestCommand(t *testing.T) {
	database, credentialVault, syncService := newSyncBundleTestService(t, false, true)
	ctx := t.Context()
	if _, err := database.AssetEnsureBuiltinLocal(ctx); err != nil {
		t.Fatal(err)
	}
	credID := ids.New()
	nonce, blob, err := credentialVault.EncryptCredential(ctx, "s3cret-pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CredentialPut(ctx, store.CredentialInput{
		ID: credID, Name: "生产口令", Kind: "password", Nonce: nonce, Blob: blob, KEKHint: credentialVault.KEKHint(),
	}); err != nil {
		t.Fatal(err)
	}
	seedSyncBundleAsset(t, database, store.AssetRow{
		ID: ids.New(), Kind: "ssh", Name: "web-01", Host: ptr("10.0.0.1"), Username: ptr("deploy"),
		CredID: &credID, OptionsJSON: "{}", CreatedAt: 100, UpdatedAt: 200,
	})
	deletedID := ids.New()
	seedSyncBundleAsset(t, database, store.AssetRow{
		ID: deletedID, Kind: "mysql", Name: "db-01", OptionsJSON: "{}", CreatedAt: 100, UpdatedAt: 150,
	})
	if err := database.AssetDelete(ctx, deletedID); err != nil {
		t.Fatal(err)
	}

	dispatcher := registerSyncBundleDispatcher(t, syncService)
	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: syncservice.CommandDigest}, ipc.Environment{})
	digest := decodeSyncBundleResponse[syncservice.SyncDigest](t, response)
	if digest.Origin != "server" || digest.Protocol != 1 || digest.AppVersion != "test-version" || digest.Desktop {
		t.Fatalf("digest header = %+v", digest)
	}
	if len(digest.Assets) != 2 {
		t.Fatalf("digest assets = %+v", digest.Assets)
	}
	byID := map[string]syncservice.DigestEntry{}
	for _, entry := range digest.Assets {
		byID[entry.ID] = entry
	}
	live, found := byID[deletedID]
	if !found || live.DeletedAt == nil {
		t.Fatalf("soft-deleted asset missing from digest: %+v", byID)
	}
	for _, entry := range digest.Assets {
		if entry.Kind == "local" {
			t.Fatalf("builtin local asset leaked into digest: %+v", entry)
		}
		if entry.Name == "web-01" && (!entry.HasCred || entry.Host == nil || *entry.Host != "10.0.0.1") {
			t.Fatalf("web-01 entry = %+v", entry)
		}
	}
}

func TestSyncBundleExportImportRoundTrip(t *testing.T) {
	sourceDB, sourceVault, sourceService := newSyncBundleTestService(t, false, true)
	ctx := t.Context()
	parentID := ids.New()
	if _, err := sourceDB.GroupUpsert(ctx, parentID, nil, "父分组", 0, 100, 100); err != nil {
		t.Fatal(err)
	}
	childID := ids.New()
	if _, err := sourceDB.GroupUpsert(ctx, childID, &parentID, "子分组", 1, 100, 110); err != nil {
		t.Fatal(err)
	}
	credID := ids.New()
	nonce, blob, err := sourceVault.EncryptCredential(ctx, "s3cret-pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sourceDB.CredentialPut(ctx, store.CredentialInput{
		ID: credID, Name: "生产口令", Kind: "password", Nonce: nonce, Blob: blob, KEKHint: sourceVault.KEKHint(),
	}); err != nil {
		t.Fatal(err)
	}
	assetID := ids.New()
	seedSyncBundleAsset(t, sourceDB, store.AssetRow{
		ID: assetID, GroupID: &childID, Kind: "ssh", Name: "web-01", Host: ptr("10.0.0.1"), Port: ptr(int32(22)),
		Username: ptr("deploy"), AuthKind: ptr("password"), CredID: &credID, OptionsJSON: `{"theme":"dark"}`,
		Tags: "prod", Note: "note", Sort: 3, CreatedAt: 100, UpdatedAt: 200,
	})
	snippetID := ids.New()
	if err := sourceDBSnippetUpsert(ctx, sourceDB, snippetID, "片段一", "echo hi", 100, 120); err != nil {
		t.Fatal(err)
	}

	sourceDispatcher := registerSyncBundleDispatcher(t, sourceService)
	export := decodeSyncBundleResponse[syncservice.SyncBundle](t, dispatchSyncBundle(t, sourceDispatcher, syncservice.CommandExport, map[string]any{
		"assetIds": []string{assetID}, "withCreds": true,
	}))
	if export.Protocol != 1 || export.Origin != "server" || export.ExportedAt <= 0 {
		t.Fatalf("bundle header = %+v", export)
	}
	if len(export.Assets) != 1 || export.Assets[0].ID != assetID || export.Assets[0].UpdatedAt != 200 {
		t.Fatalf("bundle assets = %+v", export.Assets)
	}
	if len(export.Groups) != 2 || export.Groups[0].ID != parentID || export.Groups[1].ID != childID {
		t.Fatalf("bundle groups must carry the ancestor chain root-first: %+v", export.Groups)
	}
	if len(export.Creds) != 1 || export.Creds[0].Secret != "s3cret-pw" {
		t.Fatalf("bundle creds = %+v", export.Creds)
	}
	if len(export.Snippets) != 1 || export.Snippets[0].ID != snippetID {
		t.Fatalf("bundle snippets = %+v", export.Snippets)
	}

	targetDB, targetVault, targetService := newSyncBundleTestService(t, false, true)
	targetDispatcher := registerSyncBundleDispatcher(t, targetService)
	report := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, targetDispatcher, syncservice.CommandImport, map[string]any{
		"bundle": export, "force": false,
	}))
	if report.GroupsCreated != 2 || report.AssetsCreated != 1 || report.CredsCreated != 1 || report.SnippetsCreated != 1 ||
		report.Refused != 0 || report.SkippedNewer != 0 || len(report.Warnings) != 0 {
		t.Fatalf("import report = %+v", report)
	}
	importedAsset, err := targetDB.AssetGet(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if importedAsset.GroupID == nil || *importedAsset.GroupID != childID || importedAsset.CredID == nil || *importedAsset.CredID != credID ||
		importedAsset.UpdatedAt != 200 || importedAsset.CreatedAt != 100 ||
		importedAsset.OptionsJSON != `{"theme":"dark"}` || importedAsset.Port == nil || *importedAsset.Port != 22 {
		t.Fatalf("imported asset = %+v", importedAsset)
	}
	importedCred, err := targetDB.CredentialGetRow(ctx, credID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := targetVault.DecryptCredentialString(ctx, importedCred)
	if err != nil || secret != "s3cret-pw" {
		t.Fatalf("imported cred secret = %q err=%v", secret, err)
	}
	if _, err := targetDB.SnippetGet(ctx, snippetID); err != nil {
		t.Fatal(err)
	}

	reimport := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, targetDispatcher, syncservice.CommandImport, map[string]any{
		"bundle": export, "force": false,
	}))
	if reimport.GroupsCreated != 0 || reimport.AssetsCreated != 0 || reimport.CredsCreated != 0 || reimport.SnippetsCreated != 0 ||
		reimport.GroupsUpdated != 2 || reimport.AssetsUpdated != 1 || reimport.CredsUpdated != 1 || reimport.SnippetsUpdated != 1 ||
		reimport.SkippedNewer != 0 {
		t.Fatalf("reimport report = %+v", reimport)
	}
}

func sourceDBSnippetUpsert(ctx context.Context, database *store.Store, id, name, body string, createdAt, updatedAt int64) error {
	_, err := database.DB().ExecContext(ctx, `INSERT INTO snippet(id, group_id, name, body, sort, created_at, updated_at)
VALUES(?,NULL,?,?,0,?,?)`, id, name, body, createdAt, updatedAt)
	return err
}

func TestSyncBundleExportRequiresUnlockedVaultForCreds(t *testing.T) {
	database, credentialVault, syncService := newSyncBundleTestService(t, false, true)
	ctx := t.Context()
	credID := ids.New()
	nonce, blob, err := credentialVault.EncryptCredential(ctx, "s3cret-pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CredentialPut(ctx, store.CredentialInput{
		ID: credID, Name: "生产口令", Kind: "password", Nonce: nonce, Blob: blob, KEKHint: credentialVault.KEKHint(),
	}); err != nil {
		t.Fatal(err)
	}
	assetID := ids.New()
	seedSyncBundleAsset(t, database, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "web-01", CredID: &credID, OptionsJSON: "{}", CreatedAt: 100, UpdatedAt: 200,
	})
	credentialVault.Lock()

	dispatcher := registerSyncBundleDispatcher(t, syncService)
	response := dispatchSyncBundle(t, dispatcher, syncservice.CommandExport, map[string]any{
		"assetIds": []string{assetID}, "withCreds": true,
	})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeVaultLocked {
		t.Fatalf("export with creds on locked vault = %+v, want vault_locked", response)
	}
	export := decodeSyncBundleResponse[syncservice.SyncBundle](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandExport, map[string]any{
		"assetIds": []string{assetID}, "withCreds": false,
	}))
	if len(export.Creds) != 0 || len(export.Assets) != 1 || export.Assets[0].CredID == nil || *export.Assets[0].CredID != credID {
		t.Fatalf("credential-free export = %+v", export)
	}
}

func TestSyncBundleImportMalformedInput(t *testing.T) {
	database, _, syncService := newSyncBundleTestService(t, false, true)
	dispatcher := registerSyncBundleDispatcher(t, syncService)

	truncated := dispatcher.Dispatch(context.Background(), ipc.Request{
		Command: syncservice.CommandImport, Args: json.RawMessage(`{"args":{"bundle":`),
	}, ipc.Environment{})
	if truncated.OK || truncated.Error == nil || truncated.Error.Code != ipc.CodeBadParam {
		t.Fatalf("truncated import = %+v, want bad_param", truncated)
	}

	wrongProtocol := dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": map[string]any{"protocol": 99}, "force": false,
	})
	if wrongProtocol.OK || wrongProtocol.Error == nil || wrongProtocol.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("protocol 99 import = %+v, want unsupported", wrongProtocol)
	}

	invalidID := ids.New()
	nullOptionsID := ids.New()
	report := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": map[string]any{
			"protocol": 1,
			"assets": []map[string]any{
				{"id": "bad-id", "name": "非法资产", "optionsJson": "{}", "updatedAt": 100},
				{"id": invalidID, "name": "坏选项", "optionsJson": "not-json", "updatedAt": 100},
				{"id": nullOptionsID, "name": "空选项", "optionsJson": "null", "updatedAt": 100},
			},
		},
		"force": false,
	}))
	if report.Refused != 1 || report.AssetsCreated != 2 {
		t.Fatalf("malformed entries report = %+v", report)
	}
	if _, err := database.AssetGet(t.Context(), invalidID); err != nil {
		t.Fatalf("valid asset alongside malformed one must import: %v", err)
	}
	row, err := database.AssetGet(t.Context(), invalidID)
	if err != nil || row.OptionsJSON != "{}" {
		t.Fatalf("optionsJson fallback = %+v err=%v", row, err)
	}
	nullRow, err := database.AssetGet(t.Context(), nullOptionsID)
	if err != nil || nullRow.OptionsJSON != "{}" {
		t.Fatalf("optionsJson null fallback = %+v err=%v", nullRow, err)
	}
	warned := false
	nullWarned := false
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "optionsJson") {
			warned = true
		}
		if strings.Contains(warning, nullOptionsID) {
			nullWarned = true
		}
	}
	if !warned || !nullWarned {
		t.Fatalf("optionsJson warnings missing: %+v", report.Warnings)
	}
}

func TestSyncBundleImportSkipsNewerUnlessForced(t *testing.T) {
	database, _, syncService := newSyncBundleTestService(t, false, true)
	ctx := t.Context()
	assetID := ids.New()
	seedSyncBundleAsset(t, database, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "web-01", Host: ptr("10.0.0.9"), OptionsJSON: "{}", CreatedAt: 100, UpdatedAt: 200,
	})
	dispatcher := registerSyncBundleDispatcher(t, syncService)
	bundle := map[string]any{
		"protocol": 1,
		"assets": []map[string]any{
			{"id": assetID, "name": "web-01(旧)", "host": "10.0.0.1", "optionsJson": "{}", "updatedAt": 100},
		},
	}
	report := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": bundle, "force": false,
	}))
	if report.SkippedNewer != 1 || report.AssetsUpdated != 0 || len(report.SkippedNewerDetails) != 1 {
		t.Fatalf("skip report = %+v", report)
	}
	detail := report.SkippedNewerDetails[0]
	if detail.Kind != "asset" || detail.ID != assetID || detail.LocalRevision != 200 || detail.RemoteRevision != 100 {
		t.Fatalf("skip detail = %+v", detail)
	}
	row, err := database.AssetGet(ctx, assetID)
	if err != nil || row.Host == nil || *row.Host != "10.0.0.9" {
		t.Fatalf("newer local asset must survive: %+v err=%v", row, err)
	}

	forced := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": bundle, "force": true,
	}))
	if forced.AssetsUpdated != 1 || forced.SkippedNewer != 0 {
		t.Fatalf("forced report = %+v", forced)
	}
	row, err = database.AssetGet(ctx, assetID)
	if err != nil || row.Host == nil || *row.Host != "10.0.0.1" || row.UpdatedAt != 100 {
		t.Fatalf("forced import must overwrite: %+v err=%v", row, err)
	}
}

func TestSyncBundleImportCredentialTombstones(t *testing.T) {
	database, credentialVault, syncService := newSyncBundleTestService(t, false, true)
	ctx := t.Context()
	credID := ids.New()
	nonce, blob, err := credentialVault.EncryptCredential(ctx, "s3cret-pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CredentialPut(ctx, store.CredentialInput{
		ID: credID, Name: "生产口令", Kind: "password", Nonce: nonce, Blob: blob, KEKHint: credentialVault.KEKHint(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().ExecContext(ctx, "UPDATE credential SET updated_at=200 WHERE id=?", credID); err != nil {
		t.Fatal(err)
	}
	dispatcher := registerSyncBundleDispatcher(t, syncService)
	bundle := map[string]any{
		"protocol":       1,
		"credTombstones": []map[string]any{{"id": credID, "deletedAt": 100}},
	}
	report := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": bundle, "force": false,
	}))
	if report.SkippedNewer != 1 || report.CredsDeleted != 0 {
		t.Fatalf("older tombstone report = %+v", report)
	}
	if _, err := database.CredentialGetRow(ctx, credID); err != nil {
		t.Fatalf("newer local cred must survive: %v", err)
	}

	forced := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": bundle, "force": true,
	}))
	if forced.CredsDeleted != 1 || forced.SkippedNewer != 0 {
		t.Fatalf("forced tombstone report = %+v", forced)
	}
	if _, err := database.CredentialGetRow(ctx, credID); err == nil {
		t.Fatal("forced tombstone must delete the credential row")
	}
	tombstone, err := database.CredentialTombstoneGet(ctx, credID)
	if err != nil || tombstone.DeletedAt != 100 {
		t.Fatalf("credential tombstone = %+v err=%v", tombstone, err)
	}

	missing := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": map[string]any{
			"protocol":       1,
			"credTombstones": []map[string]any{{"id": ids.New(), "deletedAt": 300}},
		},
		"force": false,
	}))
	if missing.CredsDeleted != 0 || missing.Refused != 0 {
		t.Fatalf("tombstone for missing cred = %+v", missing)
	}
}

func TestSyncBundlePeerDispatcherSurface(t *testing.T) {
	_, _, syncService := newSyncBundleTestService(t, false, false)
	dispatcher := registerSyncBundleDispatcher(t, syncService)
	registered := map[string]bool{}
	for _, command := range dispatcher.Commands() {
		registered[command] = true
	}
	for _, command := range []string{syncservice.CommandDigest, syncservice.CommandExport, syncservice.CommandImport} {
		if !registered[command] {
			t.Fatalf("main dispatcher missing %s", command)
		}
	}
	peer, err := syncService.PeerDispatcher()
	if err != nil {
		t.Fatal(err)
	}
	commands := peer.Commands()
	if len(commands) != 3 || commands[0] != syncservice.CommandDigest || commands[1] != syncservice.CommandExport || commands[2] != syncservice.CommandImport {
		t.Fatalf("peer surface = %v", commands)
	}
}

type syncBundleHTTPFixture struct {
	http     *httptest.Server
	client   *http.Client
	database *store.Store
}

func newSyncBundleHTTPFixture(t *testing.T) *syncBundleHTTPFixture {
	t.Helper()
	ctx := t.Context()
	database, _, syncService := newSyncBundleTestService(t, false, true)
	dispatcher := registerSyncBundleDispatcher(t, syncService)
	accounts := account.New(database.DB())
	transport, err := server.New(server.Config{
		Options: server.Options{
			Listen: "127.0.0.1:0", DataDir: t.TempDir(), Auth: server.AuthOn,
		},
		Dispatcher:  dispatcher,
		Accounts:    accounts,
		SyncObjects: syncService.ObjectHandler(),
		Channels: server.ChannelBinderFunc(func(string) (server.ChannelReceiver, error) {
			return nil, errors.New("no test channel")
		}),
		Version: "test-version",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(transport.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = transport.Close()
	})
	if _, err := accounts.CreateUser(ctx, "alice", "Alice", "password-a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.CreateUser(ctx, "bob", "Bob", "password-b1"); err != nil {
		t.Fatal(err)
	}
	return &syncBundleHTTPFixture{http: httpServer, client: &http.Client{}, database: database}
}

func (f *syncBundleHTTPFixture) login(t *testing.T, username, password string) (cookie *http.Cookie, csrf string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, f.http.URL+"/auth/login", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login %s status = %d", username, response.StatusCode)
	}
	var decoded struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range response.Cookies() {
		if candidate.Name == "nexterm_session" {
			cookie = candidate
		}
	}
	if cookie == nil || decoded.CSRFToken == "" {
		t.Fatalf("login %s returned no session cookie or csrf token", username)
	}
	return cookie, decoded.CSRFToken
}

func (f *syncBundleHTTPFixture) rpc(t *testing.T, command string, args any, cookie *http.Cookie, csrf string) (int, ipc.Response) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"cmd": command, "args": args})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, f.http.URL+"/rpc", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set("X-NexTerm-CSRF", csrf)
	}
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded ipc.Response
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, decoded
}

// 账号模式边界: /rpc 传输层把关会话与 CSRF; 资产库为共享工作区, 摘要/导入对所有账号一致可见。
func TestSyncBundleHTTPAuthBoundaries(t *testing.T) {
	fixture := newSyncBundleHTTPFixture(t)

	status, response := fixture.rpc(t, syncservice.CommandDigest, nil, nil, "")
	if status != http.StatusUnauthorized || response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("anonymous digest = status %d response %+v", status, response)
	}

	aliceCookie, aliceCSRF := fixture.login(t, "alice", "password-a1")
	status, response = fixture.rpc(t, syncservice.CommandDigest, nil, aliceCookie, "")
	if status != http.StatusForbidden || response.OK {
		t.Fatalf("digest without CSRF = status %d response %+v", status, response)
	}
	status, response = fixture.rpc(t, syncservice.CommandDigest, nil, aliceCookie, aliceCSRF)
	if status != http.StatusOK || !response.OK {
		t.Fatalf("alice digest = status %d response %+v", status, response)
	}
	aliceDigest := decodeSyncBundleResponse[syncservice.SyncDigest](t, response)
	if aliceDigest.Origin != "server" || aliceDigest.Protocol != 1 {
		t.Fatalf("alice digest = %+v", aliceDigest)
	}

	assetID := ids.New()
	status, response = fixture.rpc(t, syncservice.CommandImport, map[string]any{
		"args": map[string]any{
			"bundle": map[string]any{
				"protocol": 1,
				"assets": []map[string]any{
					{"id": assetID, "name": "shared-01", "optionsJson": "{}", "updatedAt": 100},
				},
			},
			"force": false,
		},
	}, aliceCookie, aliceCSRF)
	if status != http.StatusOK || !response.OK {
		t.Fatalf("alice import = status %d response %+v", status, response)
	}

	bobCookie, bobCSRF := fixture.login(t, "bob", "password-b1")
	status, response = fixture.rpc(t, syncservice.CommandDigest, nil, bobCookie, bobCSRF)
	if status != http.StatusOK || !response.OK {
		t.Fatalf("bob digest = status %d response %+v", status, response)
	}
	bobDigest := decodeSyncBundleResponse[syncservice.SyncDigest](t, response)
	found := false
	for _, entry := range bobDigest.Assets {
		if entry.ID == assetID {
			found = true
		}
	}
	if !found {
		t.Fatalf("shared library asset must be visible to every account: %+v", bobDigest.Assets)
	}
}

func ptr[T any](value T) *T { return &value }

// 本机删除墓碑优先于旧包: UI 删除(凭据经 CredentialDelete, 分组/片段经 0020 触发器)后
// 重导删除前导出的旧包, 三类对象都不得复活, 墓碑必须保留; 载荷较新或 force 才允许导入并清碑。
func TestSyncBundleImportRespectsLocalTombstones(t *testing.T) {
	database, credentialVault, syncService := newSyncBundleTestService(t, false, true)
	ctx := t.Context()

	credID := ids.New()
	nonce, blob, err := credentialVault.EncryptCredential(ctx, "s3cret-pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CredentialPut(ctx, store.CredentialInput{
		ID: credID, Name: "生产口令", Kind: "password", Nonce: nonce, Blob: blob, KEKHint: credentialVault.KEKHint(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.CredentialDelete(ctx, credID); err != nil {
		t.Fatal(err)
	}
	credTombstone, err := database.CredentialTombstoneGet(ctx, credID)
	if err != nil {
		t.Fatal(err)
	}
	if credTombstone.DeletedAt <= 0 {
		t.Fatalf("credential tombstone after UI delete = %+v", credTombstone)
	}

	groupID := ids.New()
	if _, err := database.GroupUpsert(ctx, groupID, nil, "旧分组", 0, 100, 100); err != nil {
		t.Fatal(err)
	}
	if err := database.GroupDelete(ctx, groupID); err != nil {
		t.Fatal(err)
	}
	snippetID := ids.New()
	if err := sourceDBSnippetUpsert(ctx, database, snippetID, "旧片段", "echo hi", 100, 100); err != nil {
		t.Fatal(err)
	}
	if err := database.SnippetDelete(ctx, snippetID); err != nil {
		t.Fatal(err)
	}
	groupTombstone, found, err := syncTombstoneRow(ctx, database, groupID)
	if err != nil || !found {
		t.Fatalf("group tombstone after UI delete = %+v found=%v err=%v", groupTombstone, found, err)
	}
	snippetTombstone, found, err := syncTombstoneRow(ctx, database, snippetID)
	if err != nil || !found {
		t.Fatalf("snippet tombstone after UI delete = %+v found=%v err=%v", snippetTombstone, found, err)
	}

	dispatcher := registerSyncBundleDispatcher(t, syncService)
	staleBundle := map[string]any{
		"protocol": 1,
		"groups":   []map[string]any{{"id": groupID, "name": "旧分组", "sort": 0, "createdAt": 100, "updatedAt": 100}},
		"creds":    []map[string]any{{"id": credID, "name": "生产口令", "kind": "password", "secret": "s3cret-pw", "updatedAt": 100}},
		"snippets": []map[string]any{{"id": snippetID, "name": "旧片段", "body": "echo hi", "sort": 0, "createdAt": 100, "updatedAt": 100}},
	}
	report := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": staleBundle, "force": false,
	}))
	if report.SkippedNewer != 3 || report.GroupsCreated != 0 || report.CredsCreated != 0 || report.SnippetsCreated != 0 {
		t.Fatalf("stale bundle over tombstones = %+v", report)
	}
	if _, err := database.CredentialGetRow(ctx, credID); err == nil {
		t.Fatal("stale credential resurrected over tombstone")
	}
	if _, err := database.GroupGet(ctx, groupID); err == nil {
		t.Fatal("stale group resurrected over tombstone")
	}
	if _, err := database.SnippetGet(ctx, snippetID); err == nil {
		t.Fatal("stale snippet resurrected over tombstone")
	}
	if _, err := database.CredentialTombstoneGet(ctx, credID); err != nil {
		t.Fatalf("credential tombstone must survive: %v", err)
	}
	if _, found, _ := syncTombstoneRow(ctx, database, groupID); !found {
		t.Fatal("group tombstone must survive")
	}
	if _, found, _ := syncTombstoneRow(ctx, database, snippetID); !found {
		t.Fatal("snippet tombstone must survive")
	}

	fresh := ids.NowMS() + 10000
	freshBundle := map[string]any{
		"protocol": 1,
		"groups":   []map[string]any{{"id": groupID, "name": "新分组", "sort": 0, "createdAt": 100, "updatedAt": fresh}},
		"creds":    []map[string]any{{"id": credID, "name": "新口令", "kind": "password", "secret": "new-pw", "updatedAt": fresh}},
		"snippets": []map[string]any{{"id": snippetID, "name": "新片段", "body": "echo new", "sort": 0, "createdAt": 100, "updatedAt": fresh}},
	}
	freshReport := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": freshBundle, "force": false,
	}))
	if freshReport.SkippedNewer != 0 || freshReport.GroupsCreated != 1 || freshReport.CredsCreated != 1 || freshReport.SnippetsCreated != 1 {
		t.Fatalf("newer payload over tombstone = %+v", freshReport)
	}
	if _, err := database.CredentialTombstoneGet(ctx, credID); err == nil {
		t.Fatal("newer credential import must clear its tombstone")
	}
	if _, found, _ := syncTombstoneRow(ctx, database, groupID); found {
		t.Fatal("newer group import must clear its tombstone")
	}
	if _, found, _ := syncTombstoneRow(ctx, database, snippetID); found {
		t.Fatal("newer snippet import must clear its tombstone")
	}

	if err := database.SnippetDelete(ctx, snippetID); err != nil {
		t.Fatal(err)
	}
	forcedReport := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": map[string]any{
			"protocol": 1,
			"snippets": []map[string]any{{"id": snippetID, "name": "强制片段", "body": "echo forced", "sort": 0, "createdAt": 100, "updatedAt": 100}},
		},
		"force": true,
	}))
	if forcedReport.SnippetsCreated != 1 || forcedReport.SkippedNewer != 0 {
		t.Fatalf("forced import over tombstone = %+v", forcedReport)
	}
	if row, err := database.SnippetGet(ctx, snippetID); err != nil || row.Name != "强制片段" {
		t.Fatalf("forced snippet = %+v err=%v", row, err)
	}
}

func syncTombstoneRow(ctx context.Context, database *store.Store, id string) (deletedAt int64, found bool, returnErr error) {
	err := database.DB().QueryRowContext(ctx, "SELECT deleted_at FROM sync_tombstone WHERE id = ?", id).Scan(&deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return deletedAt, true, nil
}

// 分组导入两遍链接: 子先父后不产生 FK 硬错误; 悬空父级与循环父级按 v2 文案置 nil + 警告,
// 导入整体成功并返回 ImportReport, 不再中途硬错误部分落库。
func TestSyncBundleGroupImportParentHandling(t *testing.T) {
	database, _, syncService := newSyncBundleTestService(t, false, true)
	ctx := t.Context()
	parentID := ids.New()
	childID := ids.New()
	dispatcher := registerSyncBundleDispatcher(t, syncService)

	ordered := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": map[string]any{
			"protocol": 1,
			"groups": []map[string]any{
				{"id": childID, "parentId": parentID, "name": "子分组", "sort": 1, "createdAt": 100, "updatedAt": 100},
				{"id": parentID, "name": "父分组", "sort": 0, "createdAt": 100, "updatedAt": 100},
			},
		},
		"force": false,
	}))
	if ordered.GroupsCreated != 2 || ordered.Refused != 0 {
		t.Fatalf("child-before-parent report = %+v", ordered)
	}
	child, err := database.GroupGet(ctx, childID)
	if err != nil || child.ParentID == nil || *child.ParentID != parentID {
		t.Fatalf("child group = %+v err=%v", child, err)
	}

	danglingID := ids.New()
	danglingParent := ids.New()
	dangling := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": map[string]any{
			"protocol": 1,
			"groups": []map[string]any{
				{"id": danglingID, "parentId": danglingParent, "name": "悬空父级", "sort": 0, "createdAt": 100, "updatedAt": 100},
			},
		},
		"force": false,
	}))
	if dangling.GroupsCreated != 1 {
		t.Fatalf("dangling parent report = %+v", dangling)
	}
	row, err := database.GroupGet(ctx, danglingID)
	if err != nil || row.ParentID != nil {
		t.Fatalf("dangling parent must be cleared: %+v err=%v", row, err)
	}
	if !warningsContain(dangling.Warnings, "已按顶级分组导入") {
		t.Fatalf("dangling parent warning missing: %+v", dangling.Warnings)
	}

	cycleA := ids.New()
	cycleB := ids.New()
	cycle := decodeSyncBundleResponse[syncservice.ImportReport](t, dispatchSyncBundle(t, dispatcher, syncservice.CommandImport, map[string]any{
		"bundle": map[string]any{
			"protocol": 1,
			"groups": []map[string]any{
				{"id": cycleA, "parentId": cycleB, "name": "循环甲", "sort": 0, "createdAt": 100, "updatedAt": 100},
				{"id": cycleB, "parentId": cycleA, "name": "循环乙", "sort": 0, "createdAt": 100, "updatedAt": 100},
			},
		},
		"force": false,
	}))
	if cycle.GroupsCreated != 2 {
		t.Fatalf("cycle report = %+v", cycle)
	}
	rowA, err := database.GroupGet(ctx, cycleA)
	if err != nil {
		t.Fatal(err)
	}
	rowB, err := database.GroupGet(ctx, cycleB)
	if err != nil {
		t.Fatal(err)
	}
	// 循环必须被打破: 恰好一方保持顶级(先处理的一方清父级), 另一方挂到已清父级的一方下形成合法链。
	if (rowA.ParentID == nil) == (rowB.ParentID == nil) {
		t.Fatalf("cycle must leave exactly one top-level member: A=%+v B=%+v", rowA, rowB)
	}
	if !warningsContain(cycle.Warnings, "循环") {
		t.Fatalf("cycle warning missing: %+v", cycle.Warnings)
	}
}

func warningsContain(warnings []string, substring string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substring) {
			return true
		}
	}
	return false
}
