package production

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/sshconfig"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	"gorm.io/gorm"
)

type sshImportDatabaseFixture struct {
	dispatcher   *ipc.Dispatcher
	database     *store.Store
	vault        *vault.Vault
	planner      *sshImportPlanner
	group        store.AssetGroupRow
	oldAsset     store.AssetRow
	oldCred      store.CredentialRow
	configPath   string
	backend      store.Backend
	dsn          string
	assetCreates int
}

func newSSHImportDatabaseFixture(t *testing.T) *sshImportDatabaseFixture {
	t.Helper()
	ctx := t.Context()
	fixture := dbtest.NewFixture(t)
	database := fixture.OpenStore(t)
	credentialVault := vault.Load(ctx, database)
	planner := &sshImportPlanner{database: database, vault: credentialVault, desktop: true}
	dispatcher := ipc.NewDispatcher()
	if err := registerVaultCommands(dispatcher, credentialVault, database, nil); err != nil {
		t.Fatal(err)
	}
	if err := planner.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_init_master", `{"password":"test-password"}`))
	group, err := database.GroupCreate(ctx, store.GroupInput{Name: "old-group"})
	if err != nil {
		t.Fatal(err)
	}
	oldPEM, _ := sshImportTermiusPEM(t)
	credentialID, err := putProductionCredential(ctx, credentialVault, database, "", "id_ed25519", vault.KindPrivateKey, vault.InlinePrivateKey(oldPEM, nil).Encode())
	if err != nil {
		t.Fatal(err)
	}
	oldCred, err := database.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	oldHost := "old-web.example.com"
	oldUser := "old-user"
	oldPort := int32(22)
	oldAsset, err := database.AssetCreate(ctx, store.AssetInput{
		GroupID: &group.ID, Kind: "ssh", Name: "web", Host: &oldHost, Port: &oldPort, Username: &oldUser,
		CredID: &credentialID, OptionsJSON: `{"jumpAssetId":"old-jump","keep":true}`, Tags: "old-tag", Note: "old note",
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyPath, _ := sshImportWriteKeyFile(t, dir, "id_ed25519", "")
	configPath := dir + "/config"
	content := "Host bastion\n  HostName bastion.example.com\n  User admin\n\n" +
		"Host web\n  HostName new-web.example.com\n  User deploy\n  Port 2222\n  IdentityFile " + keyPath + "\n  ProxyJump bastion\n\n" +
		"Host db\n  HostName db.example.com\n  User postgres\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return &sshImportDatabaseFixture{dispatcher: dispatcher, database: database, vault: credentialVault, planner: planner, group: group, oldAsset: oldAsset, oldCred: oldCred, configPath: configPath, backend: fixture.Backend, dsn: fixture.DSN}
}

func (f *sshImportDatabaseFixture) selection(t *testing.T) (hosts, keys []sshImportItemAction) {
	t.Helper()
	preview := sshImportPreview(t, f.dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(f.configPath)+`}}`)
	for _, alias := range []string{"bastion", "web", "db"} {
		host := previewHost(t, preview, alias)
		action := "import"
		if alias == "web" {
			if host.Action != "conflict-alias" {
				t.Fatalf("web action = %q, want conflict-alias", host.Action)
			}
			action = "overwrite"
		}
		hosts = append(hosts, sshImportItemAction{ID: host.ID, Action: action})
	}
	key := previewKey(t, preview, "id_ed25519")
	if key.Action != "conflict-alias" {
		t.Fatalf("key action = %q, want conflict-alias", key.Action)
	}
	keys = []sshImportItemAction{{ID: key.ID, Action: "overwrite"}}
	return hosts, keys
}

func (f *sshImportDatabaseFixture) apply(t *testing.T, hosts, keys []sshImportItemAction) (sshImportApplyDTO, ipc.Response) {
	t.Helper()
	return sshImportApply(t, f.dispatcher, sshImportApplyRequestBody(t, sshImportApplyRequest{
		Source: "ssh-config", Path: f.configPath, Hosts: hosts, Keys: keys,
	}))
}

func (f *sshImportDatabaseFixture) otherStore(t *testing.T) *store.Store {
	t.Helper()
	other, err := store.OpenBackend(t.Context(), store.BackendConfig{Backend: f.backend, DSN: f.dsn}, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	return other
}

func (f *sshImportDatabaseFixture) failCreates(t *testing.T, kind string) {
	t.Helper()
	callbackName := "nexterm_test:fail_ssh_import_" + kind
	err := f.database.GORM().Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		switch kind {
		case "db-host":
			if tx.Statement.Table == "asset" {
				f.assetCreates++
				if f.assetCreates == 2 {
					tx.AddError(errors.New("forced later host failure"))
				}
			}
		case "audit":
			if tx.Statement.Table == "audit_log" {
				tx.AddError(errors.New("forced audit failure"))
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *sshImportDatabaseFixture) failUpdateCAS(t *testing.T, kind string) {
	t.Helper()
	mutated := false
	callbackName := "nexterm_test:fail_ssh_import_cas_" + kind
	err := f.database.GORM().Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if mutated {
			return
		}
		switch kind {
		case "asset":
			if tx.Statement.Table != "asset" {
				return
			}
			mutated = true
			if err := tx.Exec("UPDATE asset SET updated_at = updated_at + 1 WHERE id = ?", f.oldAsset.ID).Error; err != nil {
				tx.AddError(err)
			}
		case "credential":
			if tx.Statement.Table != "credential" {
				return
			}
			mutated = true
			if err := tx.Exec("UPDATE credential SET updated_at = updated_at + 1 WHERE id = ?", f.oldCred.ID).Error; err != nil {
				tx.AddError(err)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *sshImportDatabaseFixture) assertRolledBack(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	assets, err := f.database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || !reflect.DeepEqual(assets[0], f.oldAsset) {
		t.Fatalf("assets after rollback = %+v, want original %+v", assets, f.oldAsset)
	}
	credential, err := f.database.CredentialGetRow(ctx, f.oldCred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(credential.Blob, f.oldCred.Blob) || !bytes.Equal(credential.Nonce, f.oldCred.Nonce) || credential.UpdatedAt != f.oldCred.UpdatedAt {
		t.Fatalf("credential after rollback = %+v, want original blob/version", credential)
	}
	credentials, err := f.database.CredentialList(ctx)
	if err != nil || len(credentials) != 1 {
		t.Fatalf("credentials after rollback = %+v, err=%v", credentials, err)
	}
	auditCount, err := f.database.AuditCount(ctx, store.AuditQuery{Kind: sshImportStrPtr("ssh-import")})
	if err != nil || auditCount != 0 {
		t.Fatalf("import audit rows after rollback = %d, err=%v", auditCount, err)
	}
}

func TestSSHImportLaterHostRollbackDatabase(t *testing.T) {
	fixture := newSSHImportDatabaseFixture(t)
	hosts, keys := fixture.selection(t)
	fixture.failCreates(t, "db-host")
	_, response := fixture.apply(t, hosts, keys)
	if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "回滚") {
		t.Fatalf("later host failure = %+v, want rollback error", response)
	}
	fixture.assertRolledBack(t)
}

func TestSSHImportAuditRollbackDatabase(t *testing.T) {
	fixture := newSSHImportDatabaseFixture(t)
	hosts, keys := fixture.selection(t)
	fixture.failCreates(t, "audit")
	_, response := fixture.apply(t, hosts, keys)
	if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "回滚") {
		t.Fatalf("audit failure = %+v, want rollback error", response)
	}
	fixture.assertRolledBack(t)
}

func TestSSHImportOverwritePreservesGroupAndTagsDatabase(t *testing.T) {
	fixture := newSSHImportDatabaseFixture(t)
	hosts, keys := fixture.selection(t)
	result, response := fixture.apply(t, hosts, keys)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsCreated != 2 || result.AssetsUpdated != 1 || result.CredentialsUpdated != 1 {
		t.Fatalf("apply result = %+v", result)
	}
	row, err := fixture.database.AssetGet(t.Context(), fixture.oldAsset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.GroupID == nil || *row.GroupID != fixture.group.ID || row.Tags != "old-tag" || row.Note != "old note" {
		t.Fatalf("overwrite lost group/tags/note = %+v", row)
	}
	options := assetOptions(t, row)
	if options["jumpAssetId"] == "old-jump" || options["keep"] != true {
		t.Fatalf("overwrite jump/options = %+v", options)
	}
	if options["jumpAssetId"] == nil {
		t.Fatal("overwrite did not map the imported bastion")
	}
}

func TestSSHImportRejectsSoftDeletedTargetDatabase(t *testing.T) {
	fixture := newSSHImportDatabaseFixture(t)
	plan, err := fixture.planner.derive(t.Context(), "ssh-config", fixture.configPath, false, true)
	if err != nil {
		t.Fatal(err)
	}
	hosts, keys := fixture.selection(t)
	if err := fixture.database.AssetDelete(t.Context(), fixture.oldAsset.ID); err != nil {
		t.Fatal(err)
	}
	result := sshImportApplyDTO{Warnings: []string{}}
	input := sshImportApplyRequest{Source: "ssh-config", Path: fixture.configPath, Hosts: hosts, Keys: keys}
	err = fixture.database.GORM().WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
		return fixture.planner.applyWrites(t.Context(), tx, plan, input, &result)
	})
	if err == nil || !strings.Contains(err.Error(), "重新预览") {
		t.Fatalf("soft-deleted target error = %v, want re-preview", err)
	}
	deleted, err := fixture.database.AssetGet(t.Context(), fixture.oldAsset.ID)
	if err != nil || deleted.DeletedAt == nil || deleted.Host == nil || *deleted.Host != *fixture.oldAsset.Host {
		t.Fatalf("soft-deleted asset changed = %+v, err=%v", deleted, err)
	}
	credential, err := fixture.database.CredentialGetRow(t.Context(), fixture.oldCred.ID)
	if err != nil || !bytes.Equal(credential.Blob, fixture.oldCred.Blob) {
		t.Fatalf("soft-delete changed credential = %+v, err=%v", credential, err)
	}
}

func TestSSHImportAssetCASRollbackDatabase(t *testing.T) {
	fixture := newSSHImportDatabaseFixture(t)
	hosts, keys := fixture.selection(t)
	fixture.failUpdateCAS(t, "asset")
	_, response := fixture.apply(t, hosts, keys)
	if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "重新预览") {
		t.Fatalf("asset CAS failure = %+v, want re-preview", response)
	}
	fixture.assertRolledBack(t)
}

func TestSSHImportCredentialCASRollbackDatabase(t *testing.T) {
	fixture := newSSHImportDatabaseFixture(t)
	hosts, keys := fixture.selection(t)
	fixture.failUpdateCAS(t, "credential")
	_, response := fixture.apply(t, hosts, keys)
	if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "重新预览") {
		t.Fatalf("credential CAS failure = %+v, want re-preview", response)
	}
	fixture.assertRolledBack(t)
}

func TestSSHImportCASAfterAnotherConnectionChangesDatabase(t *testing.T) {
	for _, change := range []string{"update", "delete"} {
		t.Run(change, func(t *testing.T) {
			fixture := newSSHImportDatabaseFixture(t)
			plan, err := fixture.planner.derive(t.Context(), "ssh-config", fixture.configPath, false, true)
			if err != nil {
				t.Fatal(err)
			}
			hosts, keys := fixture.selection(t)
			hostIDs, keyIDs := sshImportPreviewIDs(plan.preview)
			hostByID := map[string]*sshconfig.HostPreview{}
			for i, id := range hostIDs {
				hostByID[id] = &plan.preview.Hosts[i]
			}
			keyByID := map[string]*sshconfig.KeyPreview{}
			for i, id := range keyIDs {
				keyByID[id] = &plan.preview.Keys[i]
			}
			input := sshImportApplyRequest{Source: "ssh-config", Path: fixture.configPath, Hosts: hosts, Keys: keys}
			tx := fixture.database.GORM().WithContext(t.Context()).Begin()
			if err := fixture.planner.validateOverwriteTargets(t.Context(), tx, input, hostByID, keyByID); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			other := fixture.otherStore(t)
			if change == "update" {
				if _, err := other.AssetUpdate(t.Context(), fixture.oldAsset.ID, store.AssetPatch{Host: store.Value("other-writer.example.com")}); err != nil {
					t.Fatal(err)
				}
				if err := other.GORM().Exec(`UPDATE asset SET updated_at = updated_at + 1 WHERE id = ?`, fixture.oldAsset.ID).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := other.CredentialPut(t.Context(), store.CredentialInput{ID: fixture.oldCred.ID, Name: fixture.oldCred.Name, Kind: fixture.oldCred.Kind, Nonce: []byte{9}, Blob: []byte{9, 9, 9}}); err != nil {
					t.Fatal(err)
				}
				if err := other.GORM().Exec(`UPDATE credential SET updated_at = updated_at + 1 WHERE id = ?`, fixture.oldCred.ID).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				if err := other.AssetDelete(t.Context(), fixture.oldAsset.ID); err != nil {
					t.Fatal(err)
				}
				if err := other.CredentialDelete(t.Context(), fixture.oldCred.ID); err != nil {
					t.Fatal(err)
				}
			}
			_, err = fixture.database.AssetUpdateCAS(t.Context(), fixture.database.GORM(), fixture.oldAsset.ID, fixture.oldAsset.UpdatedAt, store.AssetPatch{Host: store.Value("import-must-not-win")})
			if !errors.Is(err, store.ErrWriteConflict) {
				t.Fatalf("asset post-precheck CAS error = %v, want ErrWriteConflict", err)
			}
			err = fixture.database.CredentialUpdateCAS(t.Context(), fixture.database.GORM(), fixture.oldCred.ID, fixture.oldCred.UpdatedAt, store.CredentialInput{Name: "must-not-win", Kind: "private_key", Nonce: []byte{1}, Blob: []byte{2}})
			if !errors.Is(err, store.ErrWriteConflict) {
				t.Fatalf("credential post-precheck CAS error = %v, want ErrWriteConflict", err)
			}
			asset, err := fixture.database.AssetGet(t.Context(), fixture.oldAsset.ID)
			if err != nil {
				t.Fatal(err)
			}
			if change == "update" {
				if asset.Host == nil || *asset.Host != "other-writer.example.com" {
					t.Fatalf("competing asset write was overwritten = %+v", asset)
				}
				credential, err := fixture.database.CredentialGetRow(t.Context(), fixture.oldCred.ID)
				if err != nil || !bytes.Equal(credential.Blob, []byte{9, 9, 9}) {
					t.Fatalf("competing credential write was overwritten = %+v, err=%v", credential, err)
				}
			} else if asset.DeletedAt == nil {
				t.Fatal("competing asset delete was overwritten")
			}
			if change == "delete" {
				if _, err := fixture.database.CredentialGetRow(t.Context(), fixture.oldCred.ID); err == nil {
					t.Fatal("competing credential delete was overwritten")
				}
			}
		})
	}
}
