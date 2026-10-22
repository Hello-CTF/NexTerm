package sync

import (
	"bytes"
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	dataupgrade "github.com/Hello-CTF/NexTerm/internal/upgrade"
)

func newUpgradeSyncDevice(t *testing.T, master bool) *testDevice {
	t.Helper()
	database := dbtest.NewFixture(t).OpenStore(t)
	if _, err := database.AssetEnsureBuiltinLocal(context.Background()); err != nil {
		t.Fatal(err)
	}
	password := ""
	if master {
		password = "device-master-pw"
	}
	credentialVault := newSyncTestVault(t, database, password)
	return &testDevice{db: database, vault: credentialVault, engine: NewEngine(database, credentialVault, nil)}
}

func syncUpgradeDevice(t *testing.T, device *testDevice, server *testSyncServer, ctx context.Context) SyncReport {
	t.Helper()
	report, err := device.engine.Sync(ctx, device.config(server, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatalf("sync after upgrade: %v (report=%+v)", err, report)
	}
	return report
}

func TestUpgradeTwoInstanceSyncContinuityDatabase(t *testing.T) {
	server := newTestSyncServerOn(t, dbtest.NewFixture(t).OpenStore(t))
	userID, _ := seedTestSyncUser(t, server.db, "alice", "alice-pw-123")
	deviceA := newUpgradeSyncDevice(t, false)
	deviceB := newUpgradeSyncDevice(t, true)
	ctx := context.Background()

	assetID := ids.New()
	putDeviceAsset(t, deviceA, store.AssetRow{ID: assetID, Kind: "ssh", Name: "before-upgrade", CreatedAt: 100, UpdatedAt: 100})
	snippetID := ids.New()
	putDeviceSnippet(t, deviceA, snippetID, "deleted-after-upgrade", "synthetic body", 100)
	credentialID := ids.New()
	putDeviceCredential(t, deviceA, credentialID, "upgraded-credential", "before-upgrade-secret", 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	beforeCursor, err := deviceA.engine.loadCursor(ctx, userID)
	if err != nil || beforeCursor.Seq == 0 {
		t.Fatalf("initial cursor = %+v, err=%v", beforeCursor, err)
	}
	beforeCursorSetting, found, err := deviceA.db.SettingGet(ctx, cursorSettingKey(userID))
	if err != nil || !found {
		t.Fatalf("initial cursor setting: found=%v err=%v", found, err)
	}
	for _, device := range []*testDevice{deviceA, deviceB} {
		if err := device.db.GORM().Create(&store.AppUser{
			ID: userID, Username: "alice", Role: "superadmin", PasswordHash: "synthetic-unused-hash",
			State: "active", CreatedAt: 1, UpdatedAt: 1,
		}).Error; err != nil {
			t.Fatal(err)
		}
		report, err := dataupgrade.Migrate(ctx, device.db.GORM(), false)
		if err != nil {
			t.Fatal(err)
		}
		if report.OwnerID != userID || report.DeferredNoActiveSuperadmin {
			t.Fatalf("device upgrade report = %+v, want owner %s", report, userID)
		}
	}
	afterCursorSetting, found, err := deviceA.db.SettingGet(ctx, cursorSettingKey(userID))
	if err != nil || !found || afterCursorSetting != beforeCursorSetting {
		t.Fatalf("upgrade changed sync cursor: before=%q after=%q found=%v err=%v", beforeCursorSetting, afterCursorSetting, found, err)
	}
	ownerCtx := WithUserID(ctx, userID)
	beforeLockedCredential, err := deviceB.db.CredentialGetRow(ownerCtx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	_, beforeLockedBlobHash, found, err := deviceB.engine.syncStateGet(ownerCtx, userID, credentialID)
	if err != nil || !found {
		t.Fatalf("initial credential reconcile state: found=%v err=%v", found, err)
	}

	future := ids.NowMS() + 3_600_000
	if _, err := deviceA.db.AssetUpsert(ownerCtx, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "after-upgrade", CreatedAt: 100, UpdatedAt: future,
	}); err != nil {
		t.Fatal(err)
	}
	nonce, blob, err := deviceA.vault.EncryptCredential(ownerCtx, "after-upgrade-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deviceA.db.CredentialPut(ownerCtx, store.CredentialInput{
		ID: credentialID, Name: "upgraded-credential", Kind: "password", Nonce: nonce, Blob: blob, KEKHint: deviceA.vault.KEKHint(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := deviceA.db.GORM().Model(&store.CredentialRow{}).
		Where("id = ? AND owner_id = ?", credentialID, userID).UpdateColumn("updated_at", future).Error; err != nil {
		t.Fatal(err)
	}
	if err := deviceA.db.SnippetDelete(ownerCtx, snippetID); err != nil {
		t.Fatal(err)
	}
	reportA := syncUpgradeDevice(t, deviceA, server, ownerCtx)
	if reportA.Pushed == 0 {
		t.Fatalf("post-upgrade instance A did not push its changes: %+v", reportA)
	}

	deviceB.vault.Lock()
	syncUpgradeDevice(t, deviceB, server, ownerCtx)
	asset, err := deviceB.db.AssetGet(ownerCtx, assetID)
	if err != nil || asset.Name != "after-upgrade" {
		t.Fatalf("post-upgrade asset did not reach instance B: %+v err=%v", asset, err)
	}
	if _, err := deviceB.db.SnippetGet(ownerCtx, snippetID); !isNotFound(err) {
		t.Fatalf("deleted snippet revived on instance B: %v", err)
	}
	lockedCredential, err := deviceB.db.CredentialGetRow(ownerCtx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(lockedCredential.Blob, beforeLockedCredential.Blob) {
		t.Fatal("locked vault applied the new credential ciphertext")
	}
	_, lockedBlobHash, found, err := deviceB.engine.syncStateGet(ownerCtx, userID, credentialID)
	if err != nil || !found || lockedBlobHash != beforeLockedBlobHash {
		t.Fatalf("locked vault credential reconcile state: found=%v err=%v", found, err)
	}

	if err := deviceB.vault.UnlockMaster(ownerCtx, "device-master-pw"); err != nil {
		t.Fatal(err)
	}
	syncUpgradeDevice(t, deviceB, server, ownerCtx)
	upgradedCredential, err := deviceB.db.CredentialGetRow(ownerCtx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := deviceB.vault.DecryptCredentialString(ownerCtx, upgradedCredential)
	if err != nil || secret != "after-upgrade-secret" {
		t.Fatalf("post-upgrade credential secret = %q, err=%v", secret, err)
	}
	syncUpgradeDevice(t, deviceA, server, ownerCtx)
	syncUpgradeDevice(t, deviceB, server, ownerCtx)
	if _, err := deviceB.db.SnippetGet(ownerCtx, snippetID); !isNotFound(err) {
		t.Fatalf("tombstoned snippet revived after later rounds: %v", err)
	}
	if _, found, err := deviceB.engine.syncTombstoneGet(ownerCtx, snippetID); err != nil || !found {
		t.Fatalf("snippet tombstone missing after later rounds: found=%v err=%v", found, err)
	}
	afterCursor, err := deviceA.engine.loadCursor(ownerCtx, userID)
	if err != nil || afterCursor.Seq <= beforeCursor.Seq {
		t.Fatalf("sync cursor did not advance across upgrade: before=%+v after=%+v err=%v", beforeCursor, afterCursor, err)
	}
	for name, report := range map[string]SyncReport{
		"A": syncUpgradeDevice(t, deviceA, server, ownerCtx),
		"B": syncUpgradeDevice(t, deviceB, server, ownerCtx),
	} {
		if report.Pulled+report.Applied+report.Pushed != 0 {
			t.Fatalf("instance %s is not quiescent after convergence: %+v", name, report)
		}
	}
}
