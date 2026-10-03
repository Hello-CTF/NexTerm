package sync

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func TestCredentialTransferReencryptsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, true)
	target := newTestInstance(t, true)
	groupID, credentialID := ids.New(), ids.New()
	putTestGroup(t, source, groupID, nil, "credentials")
	putTestCredential(t, source, credentialID, "root password", "password", "s3cret-中文")
	asset := putTestAsset(t, source, store.AssetRow{
		GroupID: &groupID, CredID: &credentialID, Name: "web", AuthKind: testPtr("password"), CreatedAt: 1, UpdatedAt: 2,
	})
	sourceRow, err := source.db.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}

	bundle, err := source.service.Export(ctx, ExportRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil || len(bundle.Credentials) != 1 {
		t.Fatalf("export bundle=%+v err=%v", bundle, err)
	}
	if bundle.Credentials[0].Secret != "s3cret-中文" {
		t.Fatal("source plaintext was not recovered for protected transfer")
	}
	report, err := target.service.Import(ctx, ImportRequest{Bundle: bundle})
	if err != nil || report.CredsCreated != 1 || report.AssetsCreated != 1 || report.GroupsCreated != 1 {
		t.Fatalf("first import report=%+v err=%v", report, err)
	}
	targetRow, err := target.db.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sourceRow.Blob, targetRow.Blob) || bytes.Equal(sourceRow.Nonce, targetRow.Nonce) {
		t.Fatal("destination reused source ciphertext instead of re-encrypting")
	}
	if _, err := target.vault.DecryptCredential(ctx, sourceRow); err == nil {
		t.Fatal("different destination vault unexpectedly decrypts source ciphertext")
	}
	plaintext, err := target.vault.DecryptCredentialString(ctx, targetRow)
	if err != nil || plaintext != "s3cret-中文" {
		t.Fatalf("destination plaintext=%q err=%v", plaintext, err)
	}

	report, err = target.service.Import(ctx, ImportRequest{Bundle: bundle})
	if err != nil || report.CredsUpdated != 1 || report.AssetsUpdated != 1 || report.GroupsUpdated != 1 ||
		report.CredsCreated != 0 || report.AssetsCreated != 0 || report.GroupsCreated != 0 {
		t.Fatalf("idempotent import report=%+v err=%v", report, err)
	}
	credentials, _ := target.db.CredentialList(ctx)
	assets, _ := target.db.AssetList(ctx, true)
	if len(credentials) != 1 || len(assets) != 2 {
		t.Fatalf("repeat sync duplicated records: creds=%d assets=%d", len(credentials), len(assets))
	}
}

func TestVaultLockRulesRejectSecretsBeforeAnyWrites(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, true)
	target := newTestInstance(t, true)
	groupID, credentialID := ids.New(), ids.New()
	putTestGroup(t, source, groupID, nil, "locked")
	putTestCredential(t, source, credentialID, "secret", "password", "value")
	asset := putTestAsset(t, source, store.AssetRow{GroupID: &groupID, CredID: &credentialID, UpdatedAt: 1})
	bundle, err := source.service.Export(ctx, ExportRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil {
		t.Fatal(err)
	}

	source.vault.Lock()
	_, err = source.service.Export(ctx, ExportRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	requireCode(t, err, ipc.CodeVaultLocked)
	if _, err := source.service.Export(ctx, ExportRequest{AssetIDs: []string{asset.ID}}); err != nil {
		t.Fatalf("credential-free export should not need vault: %v", err)
	}

	target.vault.Lock()
	_, err = target.service.Import(ctx, ImportRequest{Bundle: bundle})
	requireCode(t, err, ipc.CodeVaultLocked)
	if groups, _ := target.db.GroupList(ctx); len(groups) != 0 {
		t.Fatal("locked destination wrote groups before vault rejection")
	}
	if credentials, _ := target.db.CredentialList(ctx); len(credentials) != 0 {
		t.Fatal("locked destination wrote credentials")
	}
	if _, err := target.db.AssetGet(ctx, asset.ID); !isNotFound(err) {
		t.Fatalf("locked destination wrote asset: %v", err)
	}
	withoutCredentials := bundle
	withoutCredentials.Credentials = nil
	if _, err := target.service.Import(ctx, ImportRequest{Bundle: withoutCredentials}); err != nil {
		t.Fatalf("credential-free import should work while locked: %v", err)
	}
}

func TestFileBackedKeyConversionIsDeterministicAndWarns(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, true)
	target := newTestInstance(t, true)
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, []byte("PRIVATE KEY BODY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	asset := putTestAsset(t, source, store.AssetRow{
		Name: "key asset", AuthKind: testPtr("key"), KeyPath: &path, UpdatedAt: 1,
	})

	first, err := source.service.Export(ctx, ExportRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.service.Export(ctx, ExportRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Credentials) != 1 || len(second.Credentials) != 1 || first.Credentials[0].ID != second.Credentials[0].ID {
		t.Fatalf("derived key credential is not deterministic: first=%+v second=%+v", first.Credentials, second.Credentials)
	}
	derived := first.Credentials[0]
	if derived.ID != "synckey-"+asset.ID {
		t.Fatalf("unexpected derived credential %+v", derived)
	}
	if first.Assets[0].KeyPath != nil || first.Assets[0].CredID == nil || *first.Assets[0].CredID != derived.ID {
		t.Fatalf("asset was not converted to inline key: %+v", first.Assets[0])
	}
	payload := vault.ParsePrivateKeyPayload(derived.Secret)
	if payload.Key == nil || *payload.Key != "PRIVATE KEY BODY\n" || payload.File != nil || payload.Passphrase != nil {
		t.Fatalf("private key payload lost content: %+v", payload)
	}
	report, err := target.service.Import(ctx, ImportRequest{Bundle: first})
	if err != nil || report.CredsCreated != 1 {
		t.Fatalf("derived key import report=%+v err=%v", report, err)
	}
	stored, _ := target.db.CredentialGetRow(ctx, derived.ID)
	plaintext, err := target.vault.DecryptCredentialString(ctx, stored)
	if err != nil || plaintext != derived.Secret {
		t.Fatalf("destination key plaintext=%q err=%v", plaintext, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fallback, err := source.service.Export(ctx, ExportRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil || len(fallback.Warnings) != 1 || fallback.Assets[0].KeyPath == nil {
		t.Fatalf("missing file fallback bundle=%+v err=%v", fallback, err)
	}
	if len(fallback.Credentials) != 0 {
		t.Fatalf("missing file fallback generated credentials: %+v", fallback.Credentials)
	}
	report, err = target.service.Import(ctx, ImportRequest{Bundle: fallback})
	if err != nil || len(report.Warnings) < 2 {
		t.Fatalf("source/destination key warnings were not surfaced: %+v err=%v", report, err)
	}
}

func TestReferencedPrivateKeyInliningAndWarnings(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, true)
	target := newTestInstance(t, true)
	path := filepath.Join(t.TempDir(), "referenced-key")
	if err := os.WriteFile(path, []byte("REFERENCED KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	credentialID := ids.New()
	reference := vault.ReferencedPrivateKey(path, testPtr("secret-pass")).Encode()
	putTestCredential(t, source, credentialID, "referenced", vault.KindPrivateKey, reference)
	asset := putTestAsset(t, source, store.AssetRow{CredID: &credentialID, AuthKind: testPtr("key"), UpdatedAt: 1})

	bundle, err := source.service.Export(ctx, ExportRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil || len(bundle.Warnings) != 0 {
		t.Fatalf("reference export bundle=%+v err=%v", bundle, err)
	}
	inlined := vault.ParsePrivateKeyPayload(bundle.Credentials[0].Secret)
	if inlined.Key == nil || *inlined.Key != "REFERENCED KEY" || inlined.File != nil ||
		inlined.Passphrase == nil || *inlined.Passphrase != "secret-pass" {
		t.Fatalf("referenced key was not inlined: %+v", inlined)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	bundle, err = source.service.Export(ctx, ExportRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil || len(bundle.Warnings) != 1 {
		t.Fatalf("missing referenced key should warn at export: %+v err=%v", bundle, err)
	}
	stillReferenced := vault.ParsePrivateKeyPayload(bundle.Credentials[0].Secret)
	if stillReferenced.File == nil || *stillReferenced.File != path {
		t.Fatal("unreadable reference was silently dropped")
	}
	report, err := target.service.Import(ctx, ImportRequest{Bundle: bundle})
	if err != nil || len(report.Warnings) < 2 {
		t.Fatalf("missing destination reference should warn: %+v err=%v", report, err)
	}
	joined := strings.Join(report.Warnings, "\n")
	if !strings.Contains(joined, path) {
		t.Fatalf("warning lacks actionable path: %v", report.Warnings)
	}
}
