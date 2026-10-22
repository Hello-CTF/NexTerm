package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func inventoryTestItem(key, name, host string) InventoryItem {
	auth := "agent"
	return InventoryItem{
		Key: key, Kind: "ssh", Name: name, Host: ptr(host), Port: ptr(int32(22)), Username: ptr("deploy"), AuthKind: &auth,
		Options: map[string]any{"jumpAssetId": "bastion", "inventoryTarget": map[string]any{"alias": key}},
	}
}

func inventoryTestRowByKey(t *testing.T, db *Store, ctx context.Context, key string, sources ...string) AssetRow {
	t.Helper()
	source := InventorySourceSSHConfig
	if len(sources) > 0 {
		source = sources[0]
	}
	rows, err := db.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		options, err := decodeInventoryOptions(row.OptionsJSON)
		if err != nil {
			continue
		}
		meta, ok, err := inventoryMetaFromOptions(options)
		if err == nil && ok && meta.Source == source && meta.Key == key {
			return row
		}
	}
	t.Fatalf("managed asset %s/%q not found in %+v", source, key, rows)
	return AssetRow{}
}

func TestInventorySyncIdempotentUpdateRemoveAndManualIsolation(t *testing.T) {
	db := testStore(t)
	ctx := ipc.WithUserID(context.Background(), "inventory-owner")
	manual, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "web-a", Host: ptr("old.example"), OptionsJSON: "{}", Tags: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	dockerResult, err := db.InventorySync(ctx, InventorySourceDockerContext, "", []InventoryItem{{Key: "web-a", Kind: "docker-context", Name: "web-a"}})
	if err != nil || dockerResult.Created != 1 {
		t.Fatalf("other source sync = %+v, err=%v", dockerResult, err)
	}
	firstItems := []InventoryItem{inventoryTestItem("web-a", "web-a", "10.0.0.1"), inventoryTestItem("web-b", "web-b", "10.0.0.2")}
	first, err := db.InventorySync(ctx, InventorySourceSSHConfig, "", firstItems)
	if err != nil || first.Created != 2 || first.GroupID == "" {
		t.Fatalf("first sync = %+v, err=%v", first, err)
	}
	firstA := inventoryTestRowByKey(t, db, ctx, "web-a")
	firstB := inventoryTestRowByKey(t, db, ctx, "web-b")
	second, err := db.InventorySync(ctx, InventorySourceSSHConfig, "", firstItems)
	if err != nil || second.Created != 0 || second.Updated != 0 || second.Removed != 0 || second.Unchanged != 2 {
		t.Fatalf("idempotent sync = %+v, err=%v", second, err)
	}
	secondA := inventoryTestRowByKey(t, db, ctx, "web-a")
	if secondA.ID != firstA.ID || secondA.UpdatedAt != firstA.UpdatedAt {
		t.Fatalf("idempotent sync rewrote asset: first=%+v second=%+v", firstA, secondA)
	}

	changed := inventoryTestItem("web-a", "web-a-renamed", "10.0.1.1")
	third, err := db.InventorySync(ctx, InventorySourceSSHConfig, first.GroupID, []InventoryItem{changed})
	if err != nil || third.Updated != 1 || third.Removed != 1 || third.Created != 0 {
		t.Fatalf("update/remove sync = %+v, err=%v", third, err)
	}
	updatedA := inventoryTestRowByKey(t, db, ctx, "web-a")
	if updatedA.ID != firstA.ID || updatedA.Name != "web-a-renamed" || updatedA.Host == nil || *updatedA.Host != "10.0.1.1" {
		t.Fatalf("source-owned update = %+v", updatedA)
	}
	removedB, err := db.AssetGet(ctx, firstB.ID)
	if err != nil || removedB.DeletedAt == nil {
		t.Fatalf("removed asset was not soft deleted: %+v, err=%v", removedB, err)
	}
	manualAfter, err := db.AssetGet(ctx, manual.ID)
	if err != nil || manualAfter.DeletedAt != nil || manualAfter.UpdatedAt != manual.UpdatedAt || manualAfter.Tags != "manual" {
		t.Fatalf("manual asset changed: %+v, err=%v", manualAfter, err)
	}
	otherSource := inventoryTestRowByKey(t, db, ctx, "web-a", InventorySourceDockerContext)
	if otherSource.ID == updatedA.ID {
		t.Fatal("test key lookup did not distinguish sources")
	}
	dockerRow, err := db.AssetGet(ctx, otherSource.ID)
	if err != nil || dockerRow.DeletedAt != nil {
		t.Fatalf("different source was removed: %+v, err=%v", dockerRow, err)
	}

	fourth, err := db.InventorySync(ctx, InventorySourceSSHConfig, first.GroupID, []InventoryItem{changed, inventoryTestItem("web-b", "web-b", "10.0.0.2")})
	if err != nil || fourth.Created != 1 || fourth.Removed != 0 {
		t.Fatalf("re-add sync = %+v, err=%v", fourth, err)
	}
	readdedB := inventoryTestRowByKey(t, db, ctx, "web-b")
	if readdedB.ID == firstB.ID || readdedB.DeletedAt != nil {
		t.Fatalf("re-added asset = %+v, old=%+v", readdedB, removedB)
	}
	history, err := db.AssetList(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	deletedCount := 0
	for _, row := range history {
		if row.ID == firstB.ID && row.DeletedAt != nil {
			deletedCount++
		}
	}
	if deletedCount != 1 {
		t.Fatalf("soft-delete history was not preserved: %+v", history)
	}
}

func TestInventorySourceOwnedFieldsReadOnly(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	result, err := db.InventorySync(ctx, InventorySourceSSHConfig, "", []InventoryItem{inventoryTestItem("web-a", "web-a", "10.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	row := inventoryTestRowByKey(t, db, ctx, "web-a")
	otherGroup, err := db.GroupCreate(ctx, GroupInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	auth := "password"
	cred := "credential"
	keyPath := "/tmp/other-key"
	badOptions := map[string]any{}
	if err := json.Unmarshal([]byte(row.OptionsJSON), &badOptions); err != nil {
		t.Fatal(err)
	}
	badOptions["jumpAssetId"] = "changed"
	badOptionsJSON, err := json.Marshal(badOptions)
	if err != nil {
		t.Fatal(err)
	}
	patches := []struct {
		name  string
		patch AssetPatch
	}{
		{name: "group", patch: AssetPatch{GroupID: Value(otherGroup.ID)}},
		{name: "name", patch: AssetPatch{Name: ptr("renamed")}},
		{name: "host", patch: AssetPatch{Host: Value("other.example")}},
		{name: "port", patch: AssetPatch{Port: Value(int32(2222))}},
		{name: "username", patch: AssetPatch{Username: Value("root")}},
		{name: "auth", patch: AssetPatch{AuthKind: Value(auth)}},
		{name: "key path", patch: AssetPatch{KeyPath: Value(keyPath)}},
		{name: "credential", patch: AssetPatch{CredID: Value(cred)}},
		{name: "metadata", patch: AssetPatch{OptionsJSON: ptr("{}")}},
		{name: "source options", patch: AssetPatch{OptionsJSON: ptr(string(badOptionsJSON))}},
	}
	for _, test := range patches {
		t.Run(test.name, func(t *testing.T) {
			if _, err := db.AssetUpdate(ctx, row.ID, test.patch); err == nil {
				t.Fatal("source-owned field update was accepted")
			}
			if _, err := db.AssetUpdateCAS(ctx, db.GORM(), row.ID, row.UpdatedAt, test.patch); err == nil {
				t.Fatal("source-owned CAS update was accepted")
			}
		})
	}
	unchanged, err := db.AssetGet(ctx, row.ID)
	if err != nil || unchanged.UpdatedAt != row.UpdatedAt || unchanged.Name != row.Name || unchanged.Host == nil || *unchanged.Host != *row.Host {
		t.Fatalf("rejected update changed asset: %+v, err=%v", unchanged, err)
	}

	allowedOptions := map[string]any{}
	if err := json.Unmarshal([]byte(row.OptionsJSON), &allowedOptions); err != nil {
		t.Fatal(err)
	}
	allowedOptions["startupCommand"] = "uptime"
	allowedOptionsJSON, err := json.Marshal(allowedOptions)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := db.AssetUpdate(ctx, row.ID, AssetPatch{
		GroupID: Value(result.GroupID), Name: ptr(row.Name), Host: Value(*row.Host), Port: Value(*row.Port), Username: Value(*row.Username),
		Tags: ptr("prod, web"), Note: ptr("user note"), Sort: ptr(int64(7)), OptionsJSON: ptr(string(allowedOptionsJSON)),
	})
	if err != nil || allowed.Tags != "prod, web" || allowed.Note != "user note" || allowed.Sort != 7 {
		t.Fatalf("user-owned fields were rejected: %+v, err=%v", allowed, err)
	}

	portResults, err := db.AssetBulkEdit(ctx, []AssetBulkEditTarget{{AssetID: row.ID, ExpectedUpdatedAt: allowed.UpdatedAt}}, AssetBulkEditPatch{Port: Value(int32(2222))})
	if err != nil || len(portResults) != 1 || portResults[0].Err == nil {
		t.Fatalf("bulk source-owned port edit = %+v, err=%v", portResults, err)
	}
	tagResults, err := db.AssetBulkEdit(ctx, []AssetBulkEditTarget{{AssetID: row.ID, ExpectedUpdatedAt: allowed.UpdatedAt}}, AssetBulkEditPatch{Tags: ptr("bulk-tag")})
	if err != nil || len(tagResults) != 1 || tagResults[0].Err != nil || tagResults[0].Asset == nil || tagResults[0].Asset.Tags != "bulk-tag" {
		t.Fatalf("bulk user-owned tag edit = %+v, err=%v", tagResults, err)
	}
	if _, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "forged", OptionsJSON: `{"inventory":{"source":"ssh-config","key":"web-b"}}`}); err == nil {
		t.Fatal("manual asset forged inventory metadata")
	}
	manual, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "manual", Host: ptr("old"), OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AssetUpdate(ctx, manual.ID, AssetPatch{Host: Value("new")}); err != nil {
		t.Fatalf("manual asset source field was rejected: %v", err)
	}
	if _, err := db.AssetUpdateCAS(ctx, db.GORM(), row.ID, tagResults[0].Asset.UpdatedAt-1, AssetPatch{Tags: ptr("stale")}); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
}

func TestInventorySyncOwnerIsolation(t *testing.T) {
	db := testStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	item := inventoryTestItem("shared-key", "shared-name", "10.0.0.1")
	resultA, err := db.InventorySync(ctxA, InventorySourceSSHConfig, "", []InventoryItem{item})
	if err != nil {
		t.Fatal(err)
	}
	resultB, err := db.InventorySync(ctxB, InventorySourceSSHConfig, "", []InventoryItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if resultA.GroupID == resultB.GroupID {
		t.Fatal("different owners shared a managed group")
	}
	rowA := inventoryTestRowByKey(t, db, ctxA, "shared-key")
	rowB := inventoryTestRowByKey(t, db, ctxB, "shared-key")
	if rowA.ID == rowB.ID || rowA.OwnerID != "owner-a" || rowB.OwnerID != "owner-b" {
		t.Fatalf("owner assets = A %+v, B %+v", rowA, rowB)
	}
	if _, err := db.AssetGet(ctxB, rowA.ID); err == nil {
		t.Fatal("other owner read managed asset")
	}
	if _, err := db.InventorySync(ctxA, InventorySourceSSHConfig, resultB.GroupID, []InventoryItem{item}); err == nil {
		t.Fatal("other owner's group was accepted for inventory sync")
	}
	if _, err := db.InventorySync(ctxA, InventorySourceSSHConfig, resultA.GroupID, nil); err != nil {
		t.Fatal(err)
	}
	removedA, err := db.AssetGet(ctxA, rowA.ID)
	if err != nil || removedA.DeletedAt == nil {
		t.Fatalf("owner A asset was not removed: %+v, err=%v", removedA, err)
	}
	remainingB, err := db.AssetGet(ctxB, rowB.ID)
	if err != nil || remainingB.DeletedAt != nil {
		t.Fatalf("owner A sync affected owner B: %+v, err=%v", remainingB, err)
	}
}

func TestInventoryUpsertRejectsForgeryAndSourceWrites(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	if _, err := db.InventorySync(ctx, InventorySourceSSHConfig, "", []InventoryItem{inventoryTestItem("web-a", "web-a", "10.0.0.1")}); err != nil {
		t.Fatal(err)
	}
	managed := inventoryTestRowByKey(t, db, ctx, "web-a")
	forged := AssetRow{
		ID: ids.New(), Kind: "ssh", Name: "forged", Host: ptr("forged.example"), OptionsJSON: `{"inventory":{"source":"ssh-config","key":"host/forged"}}`,
		CreatedAt: 1, UpdatedAt: managed.UpdatedAt + 1000,
	}
	if _, err := db.AssetUpsert(ctx, forged); err == nil {
		t.Fatal("client upsert forged a new managed asset")
	}
	if _, err := db.AssetGet(ctx, forged.ID); err == nil {
		t.Fatal("forged managed asset was persisted")
	}

	tampered := managed
	tampered.Host = ptr("attacker.example")
	tampered.Tags = "attacker-controlled"
	tampered.UpdatedAt = managed.UpdatedAt + 1000
	if _, err := db.AssetUpsert(ctx, tampered); err == nil {
		t.Fatal("higher revision changed a source-owned endpoint")
	}
	metadataTampered := managed
	metadataTampered.OptionsJSON = "{}"
	metadataTampered.UpdatedAt = managed.UpdatedAt + 1000
	if _, err := db.AssetUpsert(ctx, metadataTampered); err == nil {
		t.Fatal("higher revision removed inventory metadata")
	}
	kindTampered := managed
	kindTampered.Kind = "local"
	kindTampered.UpdatedAt = managed.UpdatedAt + 1000
	if _, err := db.AssetUpsert(ctx, kindTampered); err == nil {
		t.Fatal("higher revision changed managed asset kind")
	}
	unchanged, err := db.AssetGet(ctx, managed.ID)
	if err != nil || unchanged.Host == nil || *unchanged.Host != "10.0.0.1" || unchanged.Tags == "attacker-controlled" || unchanged.UpdatedAt != managed.UpdatedAt {
		t.Fatalf("rejected upsert changed managed asset: %+v, err=%v", unchanged, err)
	}

	userUpdate := managed
	userUpdate.Tags = "synced-tag"
	userUpdate.Note = "synced note"
	userUpdate.Sort = 9
	userUpdate.UpdatedAt = managed.UpdatedAt + 1000
	if _, err := db.AssetUpsert(ctx, userUpdate); err != nil {
		t.Fatalf("user-owned fields were blocked by inventory validation: %v", err)
	}
	updated, err := db.AssetGet(ctx, managed.ID)
	if err != nil || updated.Tags != "synced-tag" || updated.Note != "synced note" || updated.Sort != 9 || updated.Host == nil || *updated.Host != "10.0.0.1" {
		t.Fatalf("user-owned upsert = %+v, err=%v", updated, err)
	}
}
