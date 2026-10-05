package sync

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestNonForceNewerAssetProtectsCredentialPlaintext(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, true)
	target := newTestInstance(t, true)
	assetID, credentialID := ids.New(), ids.New()
	putTestCredential(t, source, credentialID, "remote-old", "password", "remote-old-secret")
	putTestCredential(t, target, credentialID, "local-new", "password", "local-new-secret")
	putTestAsset(t, source, store.AssetRow{ID: assetID, CredID: &credentialID, Name: "remote-old", UpdatedAt: 100})
	putTestAsset(t, target, store.AssetRow{ID: assetID, CredID: &credentialID, Name: "local-new", UpdatedAt: 200})
	bundle, err := source.service.Export(ctx, ExportRequest{AssetIDs: []string{assetID}, WithCredentials: true})
	if err != nil {
		t.Fatal(err)
	}

	report, err := target.service.Import(ctx, ImportRequest{Bundle: bundle})
	if err != nil || report.SkippedNewer != 1 || report.CredsCreated != 0 || report.CredsUpdated != 0 || report.AssetsUpdated != 0 {
		t.Fatalf("non-force report=%+v err=%v", report, err)
	}
	asset, _ := target.db.AssetGet(ctx, assetID)
	if asset.Name != "local-new" {
		t.Fatalf("protected asset changed: %+v", asset)
	}
	credential, _ := target.db.CredentialGetRow(ctx, credentialID)
	plaintext, err := target.vault.DecryptCredentialString(ctx, credential)
	if err != nil || plaintext != "local-new-secret" || credential.Name != "local-new" {
		t.Fatalf("protected credential plaintext=%q row=%+v err=%v", plaintext, credential, err)
	}

	report, err = target.service.Import(ctx, ImportRequest{Bundle: bundle, Force: true})
	if err != nil || report.AssetsUpdated != 1 || report.CredsUpdated != 1 {
		t.Fatalf("force report=%+v err=%v", report, err)
	}
	credential, _ = target.db.CredentialGetRow(ctx, credentialID)
	plaintext, err = target.vault.DecryptCredentialString(ctx, credential)
	if err != nil || plaintext != "remote-old-secret" {
		t.Fatalf("force should accept credential together with asset, plaintext=%q err=%v", plaintext, err)
	}
}

func TestRefusedAssetDoesNotOverwriteCredential(t *testing.T) {
	ctx := context.Background()
	target := newTestInstance(t, true)
	credentialID := ids.New()
	putTestCredential(t, target, credentialID, "local", "password", "local-secret")
	bundle := Bundle{
		Protocol: ProtocolVersion,
		Assets: []AssetPayload{{
			ID: store.BuiltinLocalAssetID, CredID: &credentialID, Name: "forged", Kind: "ssh", UpdatedAt: 999,
		}},
		Credentials: []CredentialPayload{{ID: credentialID, Name: "stale", Kind: "password", Secret: "stale-secret"}},
	}
	report, err := target.service.Import(ctx, ImportRequest{Bundle: bundle, Force: true})
	if err != nil || report.Refused != 1 || report.CredsCreated != 0 || report.CredsUpdated != 0 {
		t.Fatalf("refused report=%+v err=%v", report, err)
	}
	credential, _ := target.db.CredentialGetRow(ctx, credentialID)
	plaintext, err := target.vault.DecryptCredentialString(ctx, credential)
	if err != nil || plaintext != "local-secret" {
		t.Fatalf("refused asset changed credential plaintext=%q err=%v", plaintext, err)
	}
}

func TestRealAssetDeleteUsesEffectiveRevisionInBothDirections(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, false)
	target := newTestInstance(t, false)
	assetID := ids.New()
	putTestAsset(t, source, store.AssetRow{ID: assetID, Name: "source-edit-100", UpdatedAt: 100})
	putTestAsset(t, target, store.AssetRow{ID: assetID, Name: "target-edit-200", UpdatedAt: 200})
	if err := source.db.AssetDelete(ctx, assetID); err != nil {
		t.Fatal(err)
	}
	deleted, _ := source.db.AssetGet(ctx, assetID)
	if deleted.UpdatedAt != 100 || deleted.DeletedAt == nil || *deleted.DeletedAt <= 200 {
		t.Fatalf("real delete fixture did not preserve updated_at: %+v", deleted)
	}
	export := func() Bundle {
		bundle, err := source.service.Export(ctx, ExportRequest{AssetIDs: []string{assetID}})
		if err != nil {
			t.Fatal(err)
		}
		return bundle
	}

	report, err := target.service.Import(ctx, ImportRequest{Bundle: export()})
	if err != nil || report.SkippedNewer != 0 || report.AssetsUpdated != 1 {
		t.Fatalf("delete-versus-edit report=%+v err=%v", report, err)
	}
	local, _ := target.db.AssetGet(ctx, assetID)
	if local.DeletedAt == nil || local.UpdatedAt != 100 {
		t.Fatalf("newer tombstone was not accepted: %+v", local)
	}
	digest, err := target.service.Digest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundDigest := false
	for _, entry := range digest.Assets {
		if entry.ID == assetID {
			foundDigest = true
			if entry.DeletedAt == nil || entry.UpdatedAt != *entry.DeletedAt {
				t.Fatalf("digest does not use effective revision: %+v", entry)
			}
		}
	}
	if !foundDigest {
		t.Fatal("digest omitted tombstone")
	}

	sourceLive := deleted
	sourceLive.DeletedAt = nil
	sourceLive.Name = "remote-live-200"
	sourceLive.UpdatedAt = 200
	putTestAsset(t, source, sourceLive)
	report, err = target.service.Import(ctx, ImportRequest{Bundle: export()})
	if err != nil || report.SkippedNewer != 1 || report.AssetsUpdated != 0 {
		t.Fatalf("older live row should lose to local tombstone: %+v err=%v", report, err)
	}
	local, _ = target.db.AssetGet(ctx, assetID)
	if local.DeletedAt == nil {
		t.Fatal("older live row resurrected a newer local tombstone")
	}

	sourceLive.Name = "remote-resurrect-newer"
	sourceLive.UpdatedAt = *local.DeletedAt + 100
	putTestAsset(t, source, sourceLive)
	report, err = target.service.Import(ctx, ImportRequest{Bundle: export()})
	if err != nil || report.SkippedNewer != 0 || report.AssetsUpdated != 1 {
		t.Fatalf("newer live row report=%+v err=%v", report, err)
	}
	local, _ = target.db.AssetGet(ctx, assetID)
	if local.DeletedAt != nil || local.Name != "remote-resurrect-newer" {
		t.Fatalf("newer live row did not resurrect tombstone: %+v", local)
	}
}

func TestMergedDestinationGroupTopology(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		ctx := context.Background()
		instance := newTestInstance(t, false)
		firstID, secondID := ids.New(), ids.New()
		putTestGroup(t, instance, firstID, nil, "first")
		putTestGroup(t, instance, secondID, &firstID, "second")
		report, err := instance.service.Import(ctx, ImportRequest{Bundle: Bundle{
			Protocol: ProtocolVersion,
			Groups:   []GroupPayload{{ID: firstID, ParentID: &secondID, Name: "first remote", UpdatedAt: 2}},
		}})
		if err != nil || report.GroupsUpdated != 1 || !strings.Contains(strings.Join(report.Warnings, "\n"), "循环") {
			t.Fatalf("merged cycle report=%+v err=%v", report, err)
		}
		first, _ := instance.db.GroupGet(ctx, firstID)
		second, _ := instance.db.GroupGet(ctx, secondID)
		if first.ParentID != nil || second.ParentID == nil || *second.ParentID != firstID {
			t.Fatalf("merged cycle persisted: first=%+v second=%+v", first, second)
		}
	})

	t.Run("depth", func(t *testing.T) {
		ctx := context.Background()
		instance := newTestInstance(t, false)
		var parent *string
		lastID := ""
		for i := 0; i < 65; i++ {
			id := fmt.Sprintf("%026d", 3000+i)
			putTestGroup(t, instance, id, parent, "deep")
			parent = testPtr(id)
			lastID = id
		}
		newID := fmt.Sprintf("%026d", 9999)
		report, err := instance.service.Import(ctx, ImportRequest{Bundle: Bundle{
			Protocol: ProtocolVersion,
			Groups:   []GroupPayload{{ID: newID, ParentID: &lastID, Name: "too deep"}},
		}})
		if err != nil || report.GroupsCreated != 1 || !strings.Contains(strings.Join(report.Warnings, "\n"), "64") {
			t.Fatalf("merged depth report=%+v err=%v", report, err)
		}
		group, _ := instance.db.GroupGet(ctx, newID)
		if group.ParentID != nil {
			t.Fatalf("over-deep merged parent was retained: %+v", group)
		}
	})
}

func TestBlockedCredentialIgnoresTombstone(t *testing.T) {
	ctx := context.Background()
	target := newTestInstance(t, true)
	assetID, credentialID := ids.New(), ids.New()
	putTestCredential(t, target, credentialID, "local", "password", "local-secret")
	putTestAsset(t, target, store.AssetRow{ID: assetID, CredID: &credentialID, Name: "local-newer", UpdatedAt: 200})
	deletedAt := int64(1)
	report, err := target.service.Import(ctx, ImportRequest{Bundle: Bundle{
		Protocol: ProtocolVersion, Origin: "regression-peer",
		Assets: []AssetPayload{{
			ID: assetID, CredID: &credentialID, Name: "remote-older", Kind: "ssh", OptionsJSON: `{}`, UpdatedAt: 100,
		}},
		CredTombstones: []store.CredentialTombstone{{ID: credentialID, DeletedAt: deletedAt}},
	}})
	if err != nil || report.SkippedNewer != 1 || report.CredsDeleted != 0 {
		t.Fatalf("protected credential must ignore tombstone: %+v err=%v", report, err)
	}
	if !strings.Contains(strings.Join(report.Warnings, "\n"), "保护") {
		t.Fatalf("protection warning missing: %v", report.Warnings)
	}
	credential, _ := target.db.CredentialGetRow(ctx, credentialID)
	plaintext, err := target.vault.DecryptCredentialString(ctx, credential)
	if err != nil || plaintext != "local-secret" {
		t.Fatalf("protected credential plaintext=%q err=%v", plaintext, err)
	}
}
