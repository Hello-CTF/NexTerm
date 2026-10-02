package db

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type fakeCredentialDecryptor struct {
	plaintext string
	calls     int
}

func (f *fakeCredentialDecryptor) DecryptCredentialString(_ context.Context, row store.CredentialRow) (string, error) {
	f.calls++
	if row.ID != "credential-1" {
		return "", &unexpectedCredentialError{id: row.ID}
	}
	return f.plaintext, nil
}

type unexpectedCredentialError struct {
	id string
}

func (e *unexpectedCredentialError) Error() string {
	return "unexpected credential " + e.id
}

func TestStoreAssetResolverLoadsAssetAndDecryptsCredential(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.CredentialPut(ctx, store.CredentialInput{ID: "credential-1", Name: "database", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}}); err != nil {
		t.Fatalf("put credential: %v", err)
	}
	host, username, credID := "mysql.internal", "app", "credential-1"
	port := int32(3307)
	row, err := database.AssetCreate(ctx, store.AssetInput{
		Kind: "mysql", Name: "app db", Host: &host, Port: &port, Username: &username, CredID: &credID,
		OptionsJSON: `{"database":"app"}`,
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	decryptor := &fakeCredentialDecryptor{plaintext: "decrypted-password"}
	resolver := NewStoreAssetResolver(database, decryptor)
	asset, err := resolver.ResolveDBAsset(ctx, row.ID)
	if err != nil {
		t.Fatalf("resolve asset: %v", err)
	}
	if asset.Kind != "mysql" || asset.Host == nil || *asset.Host != host || asset.Port == nil || *asset.Port != 3307 || asset.Username == nil || *asset.Username != username || asset.Password != "decrypted-password" || asset.OptionsJSON != `{"database":"app"}` || decryptor.calls != 1 {
		t.Fatalf("asset=%+v calls=%d", asset, decryptor.calls)
	}
}

func TestStoreAssetResolverWithoutCredential(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	row, err := database.AssetCreate(ctx, store.AssetInput{Kind: "redis", Name: "local redis", OptionsJSON: `{}`})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	decryptor := &fakeCredentialDecryptor{}
	asset, err := NewStoreAssetResolver(database, decryptor).ResolveDBAsset(ctx, row.ID)
	if err != nil || asset.Password != "" || decryptor.calls != 0 {
		t.Fatalf("asset=%+v calls=%d err=%v", asset, decryptor.calls, err)
	}
}
