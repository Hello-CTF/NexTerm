package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

// The credential+asset transaction is atomic on every real backend: a direct fault
// injection into the asset write rolls the credential creates back with it (R-03).
func TestImportBundleCredentialCompensationDatabase(t *testing.T) {
	instance := newDatabaseTestInstance(t, true)
	ctx := context.Background()

	injected := errors.New("injected asset create failure")
	assetCreates := 0
	if err := instance.db.GORM().Callback().Create().Before("gorm:create").Register("test:fail-second-asset-create", func(tx *gorm.DB) {
		if tx.Statement.Table != "asset" {
			return
		}
		assetCreates++
		if assetCreates == 2 {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.db.GORM().Callback().Create().Remove("test:fail-second-asset-create") })

	credID := ids.New()
	groupID := ids.New()
	bundle := SyncBundle{
		Protocol: BundleProtocolVersion,
		Groups:   []SyncBundleGroup{{ID: groupID, Name: "g", CreatedAt: 1, UpdatedAt: 1}},
		Creds:    []SyncBundleCredential{{ID: credID, Name: "c", Kind: "password", Secret: "s", UpdatedAt: 2}},
		Assets: []SyncBundleAsset{
			{ID: ids.New(), Name: "fresh", CredID: &credID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 2},
			{ID: ids.New(), Name: "boom", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 2},
		},
	}
	if _, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle}); err == nil {
		t.Fatal("import must fail when the asset write fails")
	}
	if _, err := instance.db.CredentialGetRow(ctx, credID); !isNotFound(err) {
		t.Fatalf("credential create must roll back with the asset failure: %v", err)
	}
	assets, err := instance.db.AssetList(ctx, true)
	if err != nil || len(assets) != 1 {
		t.Fatalf("asset writes must roll back (builtin only): %+v err=%v", assets, err)
	}
	if _, err := instance.db.GroupGet(ctx, groupID); err != nil {
		t.Fatalf("group class keeps its own committed transaction: %v", err)
	}
}

// Engine upserts carry the owner guard on every real backend: a cross-owner write with an
// occupied ID is an explicit conflict and never touches the other owner's row (F-04).
func TestEngineCredentialUpsertCrossOwnerDatabase(t *testing.T) {
	instance := newDatabaseTestInstance(t, true)
	ctxA := ipc.WithUserID(context.Background(), "user-a")
	ctxB := ipc.WithUserID(context.Background(), "user-b")

	credID := ids.New()
	if err := instance.db.GORM().WithContext(ctxA).Transaction(func(tx *gorm.DB) error {
		return instance.service.engine.credentialUpsertTxBound(ctxA, tx, credentialObject{
			ID: credID, Name: "owned", Kind: "password", UpdatedAt: 100,
		}, []byte{1}, []byte{2}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	err := instance.db.GORM().WithContext(ctxB).Transaction(func(tx *gorm.DB) error {
		return instance.service.engine.credentialUpsertTxBound(ctxB, tx, credentialObject{
			ID: credID, Name: "invader", Kind: "password", UpdatedAt: 200,
		}, []byte{3}, []byte{4}, nil)
	})
	if !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("cross-owner credential upsert error = %v, want ErrWriteConflict", err)
	}
	stored, err := instance.db.CredentialGetRow(ctxA, credID)
	if err != nil || stored.Name != "owned" || stored.UpdatedAt != 100 {
		t.Fatalf("cross-owner upsert landed: %+v err=%v", stored, err)
	}
}

// A plain existing-credential bundle update must land on every real backend without any
// interleave: the revision+content CAS is exercised through the normal import path (on
// MySQL/MariaDB an unquoted `blob` in the CAS once failed with Error 1064).
func TestImportBundleExistingCredentialUpdateDatabase(t *testing.T) {
	instance := newDatabaseTestInstance(t, true)
	ctx := context.Background()

	credID := ids.New()
	seed := SyncBundle{
		Protocol: BundleProtocolVersion,
		Creds:    []SyncBundleCredential{{ID: credID, Name: "c", Kind: "password", Secret: "old-secret", UpdatedAt: 100}},
	}
	if _, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: seed}); err != nil {
		t.Fatal(err)
	}
	update := SyncBundle{
		Protocol: BundleProtocolVersion,
		Creds:    []SyncBundleCredential{{ID: credID, Name: "c2", Kind: "password", Secret: "new-secret", UpdatedAt: 200}},
	}
	report, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: update})
	if err != nil {
		t.Fatal(err)
	}
	if report.CredsUpdated != 1 || report.Conflicts != 0 {
		t.Fatalf("existing credential update report = %+v", report)
	}
	row, err := instance.db.CredentialGetRow(ctx, credID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Name != "c2" || row.UpdatedAt != 200 {
		t.Fatalf("updated credential = %+v", row)
	}
	secret, err := instance.vault.DecryptCredentialString(ctx, row)
	if err != nil || secret != "new-secret" {
		t.Fatalf("updated credential secret = %q err=%v", secret, err)
	}
}

// A credential skipped by the content CAS (a later local write won after the import's
// snapshot) must survive an asset-side conflict untouched: it is not compensated back to
// the stale snapshot and is not counted as imported.
func TestImportBundleSkippedCredentialConflictIsNotCompensatedDatabase(t *testing.T) {
	instance := newDatabaseTestInstance(t, true)
	ctx := context.Background()

	credID := ids.New()
	assetID := ids.New()
	seed := SyncBundle{
		Protocol: BundleProtocolVersion,
		Creds:    []SyncBundleCredential{{ID: credID, Name: "before", Kind: "password", Secret: "old-secret", UpdatedAt: 100}},
		Assets: []SyncBundleAsset{{
			ID: assetID, Kind: "ssh", Name: "web", CredID: &credID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100,
		}},
	}
	if _, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: seed}); err != nil {
		t.Fatal(err)
	}

	// Land a monotonic later local credential write and an asset edit between the import's
	// snapshot reads and its writes: both guarded writes must then report conflicts.
	interleaveOnceOnTableQuery(t, instance, "sync_tombstone", func() {
		nonce, blob, err := instance.vault.EncryptCredential(context.Background(), "later-secret")
		if err != nil {
			t.Errorf("interleaved credential encrypt: %v", err)
			return
		}
		if _, err := instance.db.CredentialPut(context.Background(), store.CredentialInput{
			ID: credID, Name: "later-local", Kind: "password", Nonce: nonce, Blob: blob, KEKHint: instance.vault.KEKHint(),
		}); err != nil {
			t.Errorf("interleaved credential write: %v", err)
			return
		}
		if err := instance.db.GORM().WithContext(context.Background()).
			Model(&store.AssetRow{}).Where("id = ?", assetID).
			Updates(map[string]any{"host": "later-host", "updated_at": 150}).Error; err != nil {
			t.Errorf("interleaved asset edit: %v", err)
		}
	})

	bundle := SyncBundle{
		Protocol: BundleProtocolVersion,
		Creds:    []SyncBundleCredential{{ID: credID, Name: "imported", Kind: "password", Secret: "new-secret", UpdatedAt: 200}},
		Assets: []SyncBundleAsset{{
			ID: assetID, Kind: "ssh", Name: "web", CredID: &credID, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 200,
		}},
		Snippets: []SyncBundleSnippet{{ID: ids.New(), Name: "s", Body: "b", CreatedAt: 1, UpdatedAt: 2}},
	}
	report, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	if report.Conflicts != 2 {
		t.Fatalf("import conflicts = %d, want 2: %+v", report.Conflicts, report)
	}
	if report.CredsCreated != 0 || report.CredsUpdated != 0 {
		t.Fatalf("conflict-skipped credential counted as imported: %+v", report)
	}
	row, err := instance.db.CredentialGetRow(ctx, credID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Name != "later-local" || row.UpdatedAt <= 100 {
		t.Fatalf("later local credential write was compensated away: %+v", row)
	}
	secret, err := instance.vault.DecryptCredentialString(ctx, row)
	if err != nil || secret != "later-secret" {
		t.Fatalf("credential secret = %q err=%v, want later-secret", secret, err)
	}
	asset, err := instance.db.AssetGet(ctx, assetID)
	if err != nil || asset.Host == nil || *asset.Host != "later-host" {
		t.Fatalf("interleaved asset edit overwritten: %+v err=%v", asset, err)
	}
}

// The create/update × asset-conflict compensation combos hold on every real backend: a
// credential this import actually wrote is rolled back to its pre-import state when the
// only asset referencing it conflicts at write time, and never counts as imported.
func TestImportBundleCredentialCompensationCombosDatabase(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		instance := newDatabaseTestInstance(t, true)
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
			t.Fatalf("compensated credential counted as created: %+v", report)
		}
		if _, err := instance.db.CredentialGetRow(ctx, credID); !isNotFound(err) {
			t.Fatalf("conflicted asset's exclusive credential must not be created: %v", err)
		}
	})

	t.Run("update", func(t *testing.T) {
		instance := newDatabaseTestInstance(t, true)
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
	})
}

// The planner's equal-revision LWW decision lands on every real backend: at the same
// revision the payload with the lexicographically greater hash wins and is stored — the
// store layer never re-decides the winner.
func TestEngineApplyEqualRevisionLWWLandsDatabase(t *testing.T) {
	instance := newDatabaseTestInstance(t, true)
	ctx := context.Background()

	credID := ids.New()
	remote := credentialObject{ID: credID, Name: "remote", Kind: "password", Secret: "remote-secret", UpdatedAt: 100}
	remotePayload := applyPayload(t, remote)
	var later credentialObject
	var laterPayload json.RawMessage
	for index := 0; ; index++ {
		candidate := credentialObject{ID: credID, Name: "later", Kind: "password", Secret: fmt.Sprintf("later-secret-%d", index), UpdatedAt: 100}
		candidatePayload := applyPayload(t, candidate)
		if bytes.Compare([]byte(objectPayloadHash(candidatePayload)), []byte(objectPayloadHash(remotePayload))) > 0 {
			later, laterPayload = candidate, candidatePayload
			break
		}
	}
	requireApplyResult(t, mustApplyObjects(t, instance, ApplyObject{ID: credID, Kind: KindCredential, Payload: remotePayload}).Objects[0], ApplyResultApplied)
	requireApplyResult(t, mustApplyObjects(t, instance, ApplyObject{ID: credID, Kind: KindCredential, Payload: laterPayload}).Objects[0], ApplyResultApplied)

	row, err := instance.db.CredentialGetRow(ctx, credID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := instance.vault.DecryptCredentialString(ctx, row)
	if err != nil || secret != later.Secret {
		t.Fatalf("stored secret = %q err=%v, want LWW winner %q", secret, err, later.Secret)
	}
	if row.UpdatedAt != 100 {
		t.Fatalf("stored revision = %d, want 100", row.UpdatedAt)
	}
}

// An interleaved same-revision local write between the planner's snapshot and the guarded
// write is never overwritten: the content CAS reports a conflict and the interleaved
// content survives for the next round to re-decide.
func TestEngineApplyEqualRevisionInterleaveKeepsLaterWriteDatabase(t *testing.T) {
	instance := newDatabaseTestInstance(t, true)
	ctx := context.Background()

	credID := ids.New()
	seed := credentialObject{ID: credID, Name: "seed", Kind: "password", Secret: "seed-secret", UpdatedAt: 100}
	seedPayload := applyPayload(t, seed)
	requireApplyResult(t, mustApplyObjects(t, instance, ApplyObject{ID: credID, Kind: KindCredential, Payload: seedPayload}).Objects[0], ApplyResultApplied)

	// Pick a same-revision remote payload that beats the seed snapshot per the LWW
	// tie-break, so the guarded write is actually attempted.
	var remotePayload json.RawMessage
	for index := 0; ; index++ {
		candidate := credentialObject{ID: credID, Name: "remote", Kind: "password", Secret: fmt.Sprintf("remote-secret-%d", index), UpdatedAt: 100}
		candidatePayload := applyPayload(t, candidate)
		if bytes.Compare([]byte(objectPayloadHash(candidatePayload)), []byte(objectPayloadHash(seedPayload))) > 0 {
			remotePayload = candidatePayload
			break
		}
	}

	interleaveOnceOnTableQuery(t, instance, "credential_tombstone", func() {
		if err := instance.db.GORM().WithContext(context.Background()).
			Model(&store.CredentialRow{}).Where("id = ?", credID).
			Updates(map[string]any{"name": "interleaved", "updated_at": 100}).Error; err != nil {
			t.Errorf("interleaved credential write: %v", err)
		}
	})

	entry := mustApplyObjects(t, instance, ApplyObject{ID: credID, Kind: KindCredential, Payload: remotePayload}).Objects[0]
	requireApplyResult(t, entry, ApplyResultSkipped)

	row, err := instance.db.CredentialGetRow(ctx, credID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Name != "interleaved" || row.UpdatedAt != 100 {
		t.Fatalf("interleaved same-revision write overwritten: %+v", row)
	}
	secret, err := instance.vault.DecryptCredentialString(ctx, row)
	if err != nil || secret != "seed-secret" {
		t.Fatalf("stored secret = %q err=%v, want seed-secret", secret, err)
	}
}
