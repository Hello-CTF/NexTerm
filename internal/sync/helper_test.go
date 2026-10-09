package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

type testInstance struct {
	db      *store.Store
	vault   *vault.Vault
	service *Service
}

func newTestInstance(t *testing.T, unlocked bool, options ...Option) *testInstance {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.AssetEnsureBuiltinLocal(ctx); err != nil {
		t.Fatal(err)
	}
	credentialVault := vault.Load(ctx, db)
	if unlocked {
		if err := credentialVault.InitMaster(ctx, "sync-test-master"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(credentialVault.Lock)
	}
	return &testInstance{db: db, vault: credentialVault, service: New(db, credentialVault, append([]Option{WithMetadata("test", true)}, options...)...)}
}

func testPtr[T any](value T) *T { return &value }

func float64PointersEqual(left, right *float64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func requireCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s", code)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected error code %s, got %v", code, err)
	}
}

func putTestCredential(t *testing.T, instance *testInstance, id, name, kind, secret string) {
	t.Helper()
	nonce, blob, err := instance.vault.EncryptCredential(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.db.CredentialPut(context.Background(), store.CredentialInput{
		ID: id, Name: name, Kind: kind, Nonce: nonce, Blob: blob, KEKHint: instance.vault.KEKHint(),
	}); err != nil {
		t.Fatal(err)
	}
}

func putTestGroup(t *testing.T, instance *testInstance, id string, parentID *string, name string) {
	t.Helper()
	if _, err := instance.db.GroupUpsert(context.Background(), id, parentID, name, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
}

func putTestAsset(t *testing.T, instance *testInstance, row store.AssetRow) store.AssetRow {
	t.Helper()
	created, err := instance.db.AssetUpsert(context.Background(), row)
	if err != nil {
		t.Fatal(err)
	}
	result, err := instance.db.AssetGet(context.Background(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = created
	return result
}
