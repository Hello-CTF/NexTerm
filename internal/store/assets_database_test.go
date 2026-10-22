package store_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

func TestAssetImportTransactionRollbackAndOwnerDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "asset-owner")
	otherCtx := ipc.WithUserID(context.Background(), "other-owner")
	group, err := db.GroupCreate(ctx, store.GroupInput{Name: "imports"})
	if err != nil {
		t.Fatal(err)
	}

	rollback := errors.New("force import rollback")
	err = db.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		credentialID, err := db.CredentialPutTx(ctx, tx, store.CredentialInput{
			Name: "rollback-key", Kind: "private_key", Nonce: []byte{1}, Blob: []byte{2},
		})
		if err != nil {
			return err
		}
		if _, err := db.AssetCreateTx(ctx, tx, store.AssetInput{
			GroupID: &group.ID, Kind: "ssh", Name: "rollback-host", CredID: &credentialID, OptionsJSON: "{}", Tags: "one, one",
		}); err != nil {
			return err
		}
		if err := db.AuditInsertTx(ctx, tx, store.AuditInput{Source: "user", Kind: "ssh-import", Payload: map[string]any{"host": "rollback-host"}}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("transaction error = %v, want forced rollback", err)
	}
	assets, err := db.AssetList(ctx, false)
	if err != nil || len(assets) != 0 {
		t.Fatalf("assets after rollback = %+v, err=%v", assets, err)
	}
	credentials, err := db.CredentialList(ctx)
	if err != nil || len(credentials) != 0 {
		t.Fatalf("credentials after rollback = %+v, err=%v", credentials, err)
	}
	auditCount, err := db.AuditCount(ctx, store.AuditQuery{Kind: stringPointer("ssh-import")})
	if err != nil || auditCount != 0 {
		t.Fatalf("audit rows after rollback = %d, err=%v", auditCount, err)
	}

	err = db.GORM().WithContext(otherCtx).Transaction(func(tx *gorm.DB) error {
		if _, err := db.CredentialPutTx(otherCtx, tx, store.CredentialInput{Name: "other-key", Kind: "private_key", Nonce: []byte{3}, Blob: []byte{4}}); err != nil {
			return err
		}
		_, err := db.AssetCreateTx(otherCtx, tx, store.AssetInput{GroupID: &group.ID, Kind: "ssh", Name: "foreign-host", OptionsJSON: "{}"})
		return err
	})
	assertIPCCode(t, err, ipc.CodeForbidden)
	otherAssets, err := db.AssetList(otherCtx, false)
	if err != nil || len(otherAssets) != 0 {
		t.Fatalf("other owner's assets after rejected import = %+v, err=%v", otherAssets, err)
	}
	otherCredentials, err := db.CredentialList(otherCtx)
	if err != nil || len(otherCredentials) != 0 {
		t.Fatalf("other owner's credentials after rejected import = %+v, err=%v", otherCredentials, err)
	}

	var committedID string
	err = db.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		credentialID, err := db.CredentialPutTx(ctx, tx, store.CredentialInput{Name: "committed-key", Kind: "private_key", Nonce: []byte{5}, Blob: []byte{6}})
		if err != nil {
			return err
		}
		asset, err := db.AssetCreateTx(ctx, tx, store.AssetInput{GroupID: &group.ID, Kind: "ssh", Name: "committed-host", CredID: &credentialID, OptionsJSON: "{}", Tags: "one, one", Note: "keep\nthis"})
		if err != nil {
			return err
		}
		committedID = asset.ID
		return db.AuditInsertTx(ctx, tx, store.AuditInput{Source: "user", Kind: "ssh-import", Payload: map[string]any{"host": asset.Name}})
	})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := db.AssetGet(ctx, committedID)
	if err != nil || committed.Tags != "one" || committed.Note != "keep\nthis" {
		t.Fatalf("committed asset = %+v, err=%v", committed, err)
	}
	if _, err := db.AssetGet(otherCtx, committedID); err == nil {
		t.Fatal("other owner read committed asset")
	}
	search, err := db.AssetSearchWithMeta(otherCtx, "imports")
	if err != nil || search.Total != 0 || len(search.Assets) != 0 {
		t.Fatalf("other owner group search = %+v, err=%v", search, err)
	}
	if _, err := db.AssetUpdateTx(otherCtx, db.GORM(), committedID, store.AssetPatch{Note: stringPointer("changed")}); err == nil {
		t.Fatal("other owner updated committed asset")
	}
	unchanged, err := db.AssetGet(ctx, committedID)
	if err != nil || unchanged.Note != "keep\nthis" {
		t.Fatalf("asset after cross-owner update = %+v, err=%v", unchanged, err)
	}
}

func TestAssetAndCredentialCASDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "cas-owner")
	otherCtx := ipc.WithUserID(context.Background(), "other-owner")
	host := "old.example.com"
	asset, err := db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "cas-asset", Host: &host, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := db.AssetUpdateCAS(ctx, db.GORM(), asset.ID, asset.UpdatedAt, store.AssetPatch{Host: store.Value("new.example.com")})
	if err != nil || updated.UpdatedAt <= asset.UpdatedAt {
		t.Fatalf("asset CAS success = %+v, err=%v", updated, err)
	}
	if _, err := db.AssetUpdateCAS(ctx, db.GORM(), asset.ID, asset.UpdatedAt, store.AssetPatch{Host: store.Value("stale.example.com")}); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("stale asset CAS error = %v, want ErrWriteConflict", err)
	}
	if _, err := db.AssetUpdateCAS(otherCtx, db.GORM(), asset.ID, updated.UpdatedAt, store.AssetPatch{Host: store.Value("foreign.example.com")}); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("foreign asset CAS error = %v, want ErrWriteConflict", err)
	}
	if err := db.AssetDelete(ctx, asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AssetUpdateCAS(ctx, db.GORM(), asset.ID, updated.UpdatedAt, store.AssetPatch{Host: store.Value("deleted.example.com")}); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("deleted asset CAS error = %v, want ErrWriteConflict", err)
	}
	deleted, err := db.AssetGet(ctx, asset.ID)
	if err != nil || deleted.DeletedAt == nil || deleted.Host == nil || *deleted.Host != "new.example.com" {
		t.Fatalf("deleted asset after rejected CAS = %+v, err=%v", deleted, err)
	}

	credentialID, err := db.CredentialCreateTx(ctx, db.GORM(), store.CredentialInput{Name: "cas-key", Kind: "private_key", Nonce: []byte{1}, Blob: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	oldCred, err := db.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CredentialUpdateCAS(ctx, db.GORM(), credentialID, oldCred.UpdatedAt, store.CredentialInput{Name: "cas-key", Kind: "private_key", Nonce: []byte{3}, Blob: []byte{4}}); err != nil {
		t.Fatal(err)
	}
	updatedCred, err := db.CredentialGetRow(ctx, credentialID)
	if err != nil || updatedCred.UpdatedAt <= oldCred.UpdatedAt {
		t.Fatalf("credential CAS success = %+v, err=%v", updatedCred, err)
	}
	if err := db.CredentialUpdateCAS(ctx, db.GORM(), credentialID, oldCred.UpdatedAt, store.CredentialInput{Name: "cas-key", Kind: "private_key", Nonce: []byte{5}, Blob: []byte{6}}); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("stale credential CAS error = %v, want ErrWriteConflict", err)
	}
	if _, err := db.CredentialCreateTx(ctx, db.GORM(), store.CredentialInput{ID: credentialID, Name: "replacement", Kind: "private_key", Nonce: []byte{7}, Blob: []byte{8}}); err == nil {
		t.Fatal("credential create unexpectedly replaced an existing row")
	}
	retained, err := db.CredentialGetRow(ctx, credentialID)
	if err != nil || !bytes.Equal(retained.Blob, updatedCred.Blob) || retained.UpdatedAt != updatedCred.UpdatedAt {
		t.Fatalf("credential after rejected writes = %+v, want %+v", retained, updatedCred)
	}
}

func assertIPCCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("error = %v, want IPC code %s", err, code)
	}
}

func stringPointer(value string) *string { return &value }
