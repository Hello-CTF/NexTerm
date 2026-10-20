package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

// F-04: a reconciler write landing between the sync apply's snapshot read and its write must
// not be overwritten: the guarded upsert conflicts, the object stays unreconciled, and the
// next apply converges on the fresh state.
func TestApplyAssetObjectInterleavingConflict(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()

	host := "origin.example.com"
	assetID := ids.New()
	putTestAsset(t, instance, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "web", Host: &host, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100,
	})
	stored, err := instance.db.AssetGet(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}

	// Deterministic interleaving: the first asset read during apply is followed by a direct
	// reconciler-style write that advances the row revision. The store keeps one pool
	// connection alive, so the pool always has exactly one free connection for the write.
	fired := false
	if err := instance.db.GORM().Callback().Query().After("gorm:query").Register("test:apply-interleave", func(tx *gorm.DB) {
		if fired || !strings.Contains(tx.Statement.SQL.String(), "FROM `asset`") {
			return
		}
		fired = true
		if _, err := instance.db.DB().ExecContext(ctx,
			"UPDATE asset SET host = ?, updated_at = ? WHERE id = ?", "reconciler.example.com", stored.UpdatedAt+1, assetID); err != nil {
			t.Errorf("interleaved write: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.db.GORM().Callback().Query().Remove("test:apply-interleave") })

	payloadHost := "sync.example.com"
	object := ApplyObject{ID: assetID, Kind: KindAsset, Payload: applyPayload(t, assetObject{
		ID: assetID, Kind: "ssh", Name: "web", Host: &payloadHost, OptionsJSON: "{}",
		CreatedAt: 1, UpdatedAt: stored.UpdatedAt + 1000,
	})}
	entry := mustApplyObjects(t, instance, object).Objects[0]
	if entry.Result != ApplyResultSkipped || !strings.Contains(entry.Warning, "在本机已被更新") {
		t.Fatalf("interleaved apply = %+v, want skipped with conflict warning", entry)
	}
	after, err := instance.db.AssetGet(ctx, assetID)
	if err != nil || after.Host == nil || *after.Host != "reconciler.example.com" {
		t.Fatalf("reconciler write overwritten: %+v err=%v", after, err)
	}

	// The retry reads the fresh row, the higher revision wins, and the apply lands.
	entry = mustApplyObjects(t, instance, object).Objects[0]
	if entry.Result != ApplyResultApplied {
		t.Fatalf("retry apply = %+v, want applied", entry)
	}
	final, err := instance.db.AssetGet(ctx, assetID)
	if err != nil || final.Host == nil || *final.Host != payloadHost {
		t.Fatalf("retry did not converge: %+v err=%v", final, err)
	}
}

// R-03: an asset-save failure rolls the credential creates/updates back with it — no
// orphaned credentials survive a failed import (direct fault injection via trigger).
func TestImportBundleAssetSaveFailureCompensatesCredentials(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, true)

	credID := ids.New()
	groupID := ids.New()
	freshID := ids.New()
	boomID := ids.New()
	if _, err := instance.db.DB().ExecContext(ctx, `CREATE TRIGGER fail_boom_insert BEFORE INSERT ON asset
	WHEN NEW.name = 'boom' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	bundle := SyncBundle{
		Protocol: BundleProtocolVersion,
		Groups:   []SyncBundleGroup{{ID: groupID, Name: "g", CreatedAt: 1, UpdatedAt: 1}},
		Creds:    []SyncBundleCredential{{ID: credID, Name: "c", Kind: "password", Secret: "s", UpdatedAt: 2}},
		Assets: []SyncBundleAsset{
			{ID: freshID, Name: "fresh", CredID: &credID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 2},
			{ID: boomID, Name: "boom", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 2},
		},
	}
	if _, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle}); err == nil {
		t.Fatal("import must fail when the asset write fails")
	}
	if _, err := instance.db.CredentialGetRow(ctx, credID); !isNotFound(err) {
		t.Fatalf("credential create must roll back with the asset failure: %v", err)
	}
	if _, err := instance.db.AssetGet(ctx, freshID); !isNotFound(err) {
		t.Fatalf("asset class must roll back, fresh asset leaked: %v", err)
	}
	if _, err := instance.db.GroupGet(ctx, groupID); err != nil {
		t.Fatalf("group class keeps its own committed transaction: %v", err)
	}
}

// R-03: after a successful credential replacement, the old credential that no asset
// references anymore is cleaned up; a credential still referenced elsewhere is kept.
func TestImportBundleCleansOrphanedCredentialsAfterReplacement(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, true)

	oldCredID := ids.New()
	putTestCredential(t, instance, oldCredID, "old", "password", "old-secret")
	sharedCredID := ids.New()
	putTestCredential(t, instance, sharedCredID, "shared", "password", "shared-secret")
	assetID := ids.New()
	putTestAsset(t, instance, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "web", CredID: &oldCredID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 10,
	})
	otherID := ids.New()
	putTestAsset(t, instance, store.AssetRow{
		ID: otherID, Kind: "ssh", Name: "db", CredID: &sharedCredID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 10,
	})

	newCredID := ids.New()
	bundle := SyncBundle{
		Protocol: BundleProtocolVersion,
		Creds:    []SyncBundleCredential{{ID: newCredID, Name: "new", Kind: "password", Secret: "new-secret", UpdatedAt: 20}},
		Assets: []SyncBundleAsset{
			{ID: assetID, Kind: "ssh", Name: "web", CredID: &newCredID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 20},
			{ID: otherID, Kind: "ssh", Name: "db", CredID: &sharedCredID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 20},
		},
	}
	report, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	if report.CredsOrphaned != 1 {
		t.Fatalf("orphaned credential cleanup = %d, want 1: %+v", report.CredsOrphaned, report.Warnings)
	}
	if _, err := instance.db.CredentialGetRow(ctx, oldCredID); !isNotFound(err) {
		t.Fatalf("replaced credential must be cleaned up: %v", err)
	}
	if _, err := instance.db.CredentialGetRow(ctx, sharedCredID); err != nil {
		t.Fatalf("still-referenced credential must be kept: %v", err)
	}
	rebound, err := instance.db.AssetGet(ctx, assetID)
	if err != nil || rebound.CredID == nil || *rebound.CredID != newCredID {
		t.Fatalf("asset must bind the imported credential: %+v err=%v", rebound, err)
	}
}

// A bundle that both carries a credential (bound by an imported asset) and tombstones it is
// contradictory: the binding wins, the tombstone is ignored with a visible warning.
func TestImportBundleKeepsBoundCredentialAgainstTombstone(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, true)

	credID := ids.New()
	assetID := ids.New()
	bundle := SyncBundle{
		Protocol:       BundleProtocolVersion,
		Creds:          []SyncBundleCredential{{ID: credID, Name: "c", Kind: "password", Secret: "s", UpdatedAt: 2}},
		Assets:         []SyncBundleAsset{{ID: assetID, Kind: "ssh", Name: "web", CredID: &credID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 2}},
		CredTombstones: []SyncCredentialTombstone{{ID: credID, DeletedAt: 5}},
	}
	report, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	if report.CredsDeleted != 0 {
		t.Fatalf("bound credential must not be tombstoned: %+v", report)
	}
	warned := false
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "仍被本次导入的资产引用") {
			warned = true
			break
		}
	}
	if !warned {
		t.Fatalf("ignored tombstone must stay visible: %+v", report.Warnings)
	}
	row, err := instance.db.AssetGet(ctx, assetID)
	if err != nil || row.CredID == nil || *row.CredID != credID {
		t.Fatalf("binding must survive the contradictory tombstone: %+v err=%v", row, err)
	}
	if _, err := instance.db.CredentialGetRow(ctx, credID); err != nil {
		t.Fatalf("bound credential must survive: %v", err)
	}
}
