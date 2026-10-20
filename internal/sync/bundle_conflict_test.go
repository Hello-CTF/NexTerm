package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

// interleaveAssetEdit registers a one-shot hook that lands a local edit on the asset
// between the import's snapshot read and its write. The hook fires on a First-based
// preparation query (which releases its connection before the callback runs, so the
// interleaved write can never deadlock on the two-connection pool).
func interleaveAssetEdit(t *testing.T, instance *testInstance, hookTable, host string, updatedAt int64, assetID string) {
	t.Helper()
	fired := false
	if err := instance.db.GORM().Callback().Query().After("gorm:query").Register("test:import-interleave-"+hookTable, func(tx *gorm.DB) {
		if fired || !strings.Contains(tx.Statement.SQL.String(), "FROM `"+hookTable+"`") {
			return
		}
		fired = true
		if _, err := instance.db.DB().ExecContext(context.Background(),
			"UPDATE asset SET host = ?, updated_at = ? WHERE id = ?", host, updatedAt, assetID); err != nil {
			t.Errorf("interleaved edit: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.db.GORM().Callback().Query().Remove("test:import-interleave-" + hookTable) })
}

// R-16 pre-planner interleaving: a local edit landing between the import's snapshot and
// its write advances the revision atomically, so the guarded upsert reports a conflict
// and the local content survives instead of being overwritten.
func TestImportBundlePrePlannerInterleavedEditConflicts(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()

	assetID := ids.New()
	putTestAsset(t, instance, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "web", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100,
	})
	// The snippet's tombstone check runs after the snapshot and before the write.
	interleaveAssetEdit(t, instance, "sync_tombstone", "local-edit", 101, assetID)

	bundle := SyncBundle{
		Protocol: BundleProtocolVersion,
		Assets: []SyncBundleAsset{{
			ID: assetID, Kind: "ssh", Name: "web", Host: testPtr("bundle-host"), OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 200,
		}},
		Snippets: []SyncBundleSnippet{{ID: ids.New(), Name: "s", Body: "b", CreatedAt: 1, UpdatedAt: 2}},
	}
	report, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	if report.Conflicts != 1 {
		t.Fatalf("import conflicts = %d, want 1: %+v", report.Conflicts, report)
	}
	final, err := instance.db.AssetGet(ctx, assetID)
	if err != nil || final.Host == nil || *final.Host != "local-edit" {
		t.Fatalf("interleaved edit overwritten: %+v err=%v", final, err)
	}
}

// R-03 create case: a credential referenced only by an asset that conflicts at write time
// must not be created; the conflict is reported and no orphan credential survives.
func TestImportBundleConflictDoesNotCreateOrphanCredential(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()

	assetID := ids.New()
	putTestAsset(t, instance, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "web", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100,
	})
	credID := ids.New()
	interleaveAssetEdit(t, instance, "credential_tombstone", "local-edit", 101, assetID)

	bundle := SyncBundle{
		Protocol: BundleProtocolVersion,
		Creds:    []SyncBundleCredential{{ID: credID, Name: "c", Kind: "password", Secret: "new-secret", UpdatedAt: 200}},
		Assets: []SyncBundleAsset{{
			ID: assetID, Kind: "ssh", Name: "web", CredID: &credID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 200,
		}},
	}
	report, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	if report.Conflicts != 1 {
		t.Fatalf("import conflicts = %d, want 1: %+v", report.Conflicts, report)
	}
	if report.CredsCreated != 0 {
		t.Fatalf("orphan credential counted as created: %+v", report)
	}
	if _, err := instance.db.CredentialGetRow(ctx, credID); !isNotFound(err) {
		t.Fatalf("conflicted asset's exclusive credential must not be created: %v", err)
	}
}

// R-03 absent-from-bundle case: a pre-existing local credential that the bundle's asset
// references but that is NOT part of bundle.Creds was never written by this import, so an
// asset conflict must not delete it; the local credential and its secret survive.
func TestImportBundleConflictKeepsAbsentFromBundleCredential(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()

	credID := ids.New()
	putTestCredential(t, instance, credID, "local", "password", "old-secret")
	assetID := ids.New()
	putTestAsset(t, instance, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "web", CredID: &credID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100,
	})
	// The snippet's tombstone check runs after the snapshot and before the write.
	interleaveAssetEdit(t, instance, "sync_tombstone", "local-edit", 101, assetID)

	bundle := SyncBundle{
		Protocol: BundleProtocolVersion,
		Assets: []SyncBundleAsset{{
			ID: assetID, Kind: "ssh", Name: "web", CredID: &credID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 200,
		}},
		Snippets: []SyncBundleSnippet{{ID: ids.New(), Name: "s", Body: "b", CreatedAt: 1, UpdatedAt: 2}},
	}
	report, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	if report.Conflicts != 1 {
		t.Fatalf("import conflicts = %d, want 1: %+v", report.Conflicts, report)
	}
	row, err := instance.db.CredentialGetRow(ctx, credID)
	if err != nil {
		t.Fatalf("absent-from-bundle credential must survive the asset conflict: %v", err)
	}
	secret, err := instance.vault.DecryptCredentialString(ctx, row)
	if err != nil || secret != "old-secret" {
		t.Fatalf("absent-from-bundle credential secret = %q err=%v, want old-secret", secret, err)
	}
}

// R-03 update case: an existing credential referenced only by an asset that conflicts at
// write time must keep its prior secret; the import's overwrite is compensated back.
func TestImportBundleConflictKeepsExistingCredentialSecret(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()

	credID := ids.New()
	putTestCredential(t, instance, credID, "c", "password", "old-secret")
	assetID := ids.New()
	putTestAsset(t, instance, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "web", CredID: &credID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100,
	})
	interleaveAssetEdit(t, instance, "credential_tombstone", "local-edit", 101, assetID)

	bundle := SyncBundle{
		Protocol: BundleProtocolVersion,
		Creds:    []SyncBundleCredential{{ID: credID, Name: "c", Kind: "password", Secret: "new-secret", UpdatedAt: 200}},
		Assets: []SyncBundleAsset{{
			ID: assetID, Kind: "ssh", Name: "web", CredID: &credID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 200,
		}},
	}
	report, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	if report.Conflicts != 1 {
		t.Fatalf("import conflicts = %d, want 1: %+v", report.Conflicts, report)
	}
	if report.CredsUpdated != 0 {
		t.Fatalf("compensated credential counted as updated: %+v", report)
	}
	row, err := instance.db.CredentialGetRow(ctx, credID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := instance.vault.DecryptCredentialString(ctx, row)
	if err != nil || secret != "old-secret" {
		t.Fatalf("credential secret after conflicted import = %q err=%v, want old-secret", secret, err)
	}
}

// R-03 shared case: a credential referenced by both a conflicted asset and a successfully
// written asset keeps the imported content — the shared reference is legitimate.
func TestImportBundleConflictKeepsSharedCredential(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()

	credID := ids.New()
	conflictedID := ids.New()
	putTestAsset(t, instance, store.AssetRow{
		ID: conflictedID, Kind: "ssh", Name: "web", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100,
	})
	sharedID := ids.New()
	putTestAsset(t, instance, store.AssetRow{
		ID: sharedID, Kind: "ssh", Name: "db", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100,
	})
	interleaveAssetEdit(t, instance, "credential_tombstone", "local-edit", 101, conflictedID)

	bundle := SyncBundle{
		Protocol: BundleProtocolVersion,
		Creds:    []SyncBundleCredential{{ID: credID, Name: "c", Kind: "password", Secret: "new-secret", UpdatedAt: 200}},
		Assets: []SyncBundleAsset{
			{ID: conflictedID, Kind: "ssh", Name: "web", CredID: &credID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 200},
			{ID: sharedID, Kind: "ssh", Name: "db", CredID: &credID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 200},
		},
	}
	report, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	if report.Conflicts != 1 {
		t.Fatalf("import conflicts = %d, want 1: %+v", report.Conflicts, report)
	}
	if report.CredsCreated != 1 {
		t.Fatalf("shared credential must be created: %+v", report)
	}
	row, err := instance.db.CredentialGetRow(ctx, credID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := instance.vault.DecryptCredentialString(ctx, row)
	if err != nil || secret != "new-secret" {
		t.Fatalf("shared credential secret = %q err=%v, want new-secret", secret, err)
	}
	shared, err := instance.db.AssetGet(ctx, sharedID)
	if err != nil || shared.CredID == nil || *shared.CredID != credID {
		t.Fatalf("shared asset must bind the imported credential: %+v err=%v", shared, err)
	}
}
