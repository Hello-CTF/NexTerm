package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func inventoryApplyAssetObject(row store.AssetRow) assetObject {
	return assetObject{
		ID: row.ID, GroupID: row.GroupID, Kind: row.Kind, Name: row.Name,
		Host: row.Host, Port: row.Port, Username: row.Username, AuthKind: row.AuthKind,
		KeyPath: row.KeyPath, CredID: row.CredID, OptionsJSON: row.OptionsJSON,
		Tags: row.Tags, Note: row.Note, Sort: row.Sort, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt,
	}
}

func inventoryBundleAsset(row store.AssetRow) SyncBundleAsset {
	return SyncBundleAsset{
		ID: row.ID, GroupID: row.GroupID, Kind: row.Kind, Name: row.Name,
		Host: row.Host, Port: row.Port, Username: row.Username, AuthKind: row.AuthKind,
		KeyPath: row.KeyPath, CredID: row.CredID, OptionsJSON: row.OptionsJSON,
		Tags: row.Tags, Note: row.Note, Sort: row.Sort, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt,
	}
}

func managedInventoryAsset(t *testing.T, instance *testInstance) store.AssetRow {
	t.Helper()
	ctx := context.Background()
	if _, err := instance.db.InventorySync(ctx, store.InventorySourceSSHConfig, "", []store.InventoryItem{{
		Key: "host/web-a", Kind: "ssh", Name: "web-a", Host: testPtr("10.0.0.1"), Port: testPtr(int32(22)),
		Options: map[string]any{"inventoryTarget": map[string]any{"type": "ssh-config", "alias": "web-a"}},
	}}); err != nil {
		t.Fatal(err)
	}
	rows, err := instance.db.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == "web-a" {
			return row
		}
	}
	t.Fatal("managed asset not created")
	return store.AssetRow{}
}

func TestApplyObjectsRejectsInventoryForgeryAndHighRevisionTampering(t *testing.T) {
	instance := newTestInstance(t, false)
	managed := managedInventoryAsset(t, instance)
	forged := store.AssetRow{
		ID: ids.New(), Kind: "ssh", Name: "forged", Host: testPtr("forged.example"),
		OptionsJSON: `{"inventory":{"source":"ssh-config","key":"host/forged"}}`, CreatedAt: 1, UpdatedAt: managed.UpdatedAt + 1000,
	}
	forgedResult := mustApplyObjects(t, instance, ApplyObject{ID: forged.ID, Kind: KindAsset, Payload: applyPayload(t, inventoryApplyAssetObject(forged))})
	if forgedResult.Applied != 0 || forgedResult.Skipped != 1 || !strings.Contains(forgedResult.Objects[0].Warning, "inventory") {
		t.Fatalf("forged apply result = %+v", forgedResult)
	}
	if _, err := instance.db.AssetGet(context.Background(), forged.ID); err == nil {
		t.Fatal("forged managed asset was persisted through sync apply")
	}

	tampered := managed
	tampered.Host = testPtr("attacker.example")
	tampered.UpdatedAt = managed.UpdatedAt + 1000
	tamperedResult := mustApplyObjects(t, instance, ApplyObject{ID: managed.ID, Kind: KindAsset, Payload: applyPayload(t, inventoryApplyAssetObject(tampered))})
	if tamperedResult.Applied != 0 || tamperedResult.Skipped != 1 || !strings.Contains(tamperedResult.Objects[0].Warning, "只读") {
		t.Fatalf("tampered apply result = %+v", tamperedResult)
	}
	current, err := instance.db.AssetGet(context.Background(), managed.ID)
	if err != nil || current.Host == nil || *current.Host != "10.0.0.1" || current.UpdatedAt != managed.UpdatedAt {
		t.Fatalf("high revision tampering changed asset: %+v, err=%v", current, err)
	}
}

func TestImportBundleRejectsInventoryForgeryAndRollsBack(t *testing.T) {
	for _, test := range []struct {
		name     string
		existing bool
	}{
		{name: "forged create"},
		{name: "high revision tampering", existing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			instance := newTestInstance(t, false)
			var payload store.AssetRow
			if test.existing {
				payload = managedInventoryAsset(t, instance)
				payload.Host = testPtr("attacker.example")
				payload.UpdatedAt += 1000
			} else {
				payload = store.AssetRow{
					ID: ids.New(), Kind: "ssh", Name: "forged", Host: testPtr("forged.example"),
					OptionsJSON: `{"inventory":{"source":"ssh-config","key":"host/forged"}}`, CreatedAt: 1, UpdatedAt: 1000,
				}
			}
			_, err := instance.service.ImportBundle(context.Background(), ImportBundleRequest{
				Bundle: SyncBundle{Protocol: BundleProtocolVersion, Assets: []SyncBundleAsset{inventoryBundleAsset(payload)}}, Force: true,
			})
			if err == nil {
				t.Fatal("bundle inventory validation was not enforced")
			}
			if test.existing {
				current, getErr := instance.db.AssetGet(context.Background(), payload.ID)
				if getErr != nil || current.Host == nil || *current.Host != "10.0.0.1" || current.UpdatedAt != payload.UpdatedAt-1000 {
					t.Fatalf("bundle tampering changed asset: %+v, err=%v", current, getErr)
				}
			} else if _, getErr := instance.db.AssetGet(context.Background(), payload.ID); getErr == nil {
				t.Fatal("bundle forged managed asset was persisted")
			}
		})
	}
}
