package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

// On an equal-revision LWW tie-break, a concurrent write landing between the apply's
// snapshot read and its guarded upsert must be detected as a content change: the write
// conflicts instead of overwriting the interleaved winner.
func TestApplyAssetObjectEqualRevisionReDecidesAgainstCurrentContent(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()

	assetID := "01M5C000000000000000000001"
	payloadHash := func(host string) string {
		payload, err := marshalObject(assetObject{ID: assetID, Kind: "ssh", Name: "web", Host: &host, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100})
		if err != nil {
			t.Fatal(err)
		}
		return objectPayloadHash(payload)
	}
	// Dynamically pick hosts so the remote payload wins the tie-break against the original
	// local snapshot but loses to the interleaved content.
	candidates := []string{"host-a", "host-b", "host-c", "host-d", "host-e", "host-f", "host-g", "host-h"}
	localHost := "host-local"
	remoteHost, interleavedHost := "", ""
	for _, first := range candidates {
		for _, second := range candidates {
			if first == second {
				continue
			}
			if payloadHash(first) > payloadHash(localHost) && payloadHash(second) > payloadHash(first) {
				remoteHost, interleavedHost = first, second
				break
			}
		}
		if remoteHost != "" {
			break
		}
	}
	if remoteHost == "" {
		t.Fatal("no host pair satisfies the tie-break ordering")
	}

	putTestAsset(t, instance, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "web", Host: &localHost, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100,
	})

	// A concurrent write lands between the apply's snapshot read and its guarded upsert,
	// with content that wins the hash tie-break against the remote payload.
	fired := false
	if err := instance.db.GORM().Callback().Query().After("gorm:query").Register("test:lww-interleave", func(tx *gorm.DB) {
		if fired || !strings.Contains(tx.Statement.SQL.String(), "FROM `asset`") {
			return
		}
		fired = true
		if _, err := instance.db.DB().ExecContext(ctx,
			"UPDATE asset SET host = ? WHERE id = ?", interleavedHost, assetID); err != nil {
			t.Errorf("interleaved write: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.db.GORM().Callback().Query().Remove("test:lww-interleave") })

	// The remote payload at the same revision: it beats the ORIGINAL local snapshot on the
	// hash tie-break, but loses to the interleaved content.
	payloadHost := remoteHost
	object := ApplyObject{ID: assetID, Kind: KindAsset, Payload: applyPayload(t, assetObject{
		ID: assetID, Kind: "ssh", Name: "web", Host: &payloadHost, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100,
	})}
	entry := mustApplyObjects(t, instance, object).Objects[0]
	if entry.Result == ApplyResultApplied {
		t.Fatalf("interleaved tie-break winner overwritten: result=%+v", entry)
	}
	stored, err := instance.db.AssetGet(ctx, assetID)
	if err != nil || stored.Host == nil || *stored.Host != interleavedHost {
		t.Fatalf("interleaved content overwritten: %+v err=%v", stored, err)
	}
}

// An explicit bundle credential import must not blindly overwrite a credential that
// changed after the snapshot: the write binds to the snapshot's revision, so a later
// local credential write conflicts instead of being overwritten by the import.
func TestImportBundleDoesNotBlindOverwriteLaterCredential(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()

	credID := ids.New()
	putTestCredential(t, instance, credID, "c", "password", "old-secret")

	// After the snapshot, a later local write lands with a different secret.
	fired := false
	if err := instance.db.GORM().Callback().Query().After("gorm:query").Register("test:later-cred", func(tx *gorm.DB) {
		if fired || !strings.Contains(tx.Statement.SQL.String(), "FROM `credential`") {
			return
		}
		fired = true
		if _, err := instance.db.DB().ExecContext(ctx,
			"UPDATE credential SET name = ? WHERE id = ?", "later-local", credID); err != nil {
			t.Errorf("later credential write: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.db.GORM().Callback().Query().Remove("test:later-cred") })

	bundle := SyncBundle{
		Protocol: BundleProtocolVersion,
		Creds:    []SyncBundleCredential{{ID: credID, Name: "imported", Kind: "password", Secret: "import-secret", UpdatedAt: 200}},
	}
	report, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	if report.Conflicts != 1 {
		t.Fatalf("later credential write overwritten: conflicts=%d, want 1: %+v", report.Conflicts, report)
	}
	row, err := instance.db.CredentialGetRow(ctx, credID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := instance.vault.DecryptCredentialString(ctx, row)
	if err != nil || secret != "old-secret" {
		t.Fatalf("later credential secret overwritten: %q err=%v, want old-secret", secret, err)
	}
}
