package sync

import (
	"context"
	"errors"
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
		return instance.service.engine.credentialUpsertTx(ctxA, tx, credentialObject{
			ID: credID, Name: "owned", Kind: "password", UpdatedAt: 100,
		}, []byte{1}, []byte{2}, true)
	}); err != nil {
		t.Fatal(err)
	}
	err := instance.db.GORM().WithContext(ctxB).Transaction(func(tx *gorm.DB) error {
		return instance.service.engine.credentialUpsertTx(ctxB, tx, credentialObject{
			ID: credID, Name: "invader", Kind: "password", UpdatedAt: 200,
		}, []byte{3}, []byte{4}, true)
	})
	if !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("cross-owner credential upsert error = %v, want ErrWriteConflict", err)
	}
	stored, err := instance.db.CredentialGetRow(ctxA, credID)
	if err != nil || stored.Name != "owned" || stored.UpdatedAt != 100 {
		t.Fatalf("cross-owner upsert landed: %+v err=%v", stored, err)
	}
}
