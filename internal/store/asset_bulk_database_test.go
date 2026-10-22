package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestAssetBulkEditResultsCASOwnerAndDeletedDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "bulk-owner")
	otherCtx := ipc.WithUserID(context.Background(), "other-owner")
	group, err := db.GroupCreate(ctx, store.GroupInput{Name: "fleet"})
	if err != nil {
		t.Fatal(err)
	}
	credentialID, err := db.CredentialPut(ctx, store.CredentialInput{
		Name: "fleet-credential", Kind: "private_key", Nonce: []byte{1}, Blob: []byte("bulk-secret-must-not-be-returned"),
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignCredentialID, err := db.CredentialPut(otherCtx, store.CredentialInput{
		Name: "foreign-credential", Kind: "private_key", Nonce: []byte{2}, Blob: []byte("foreign-secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	jump, err := db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "jump", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.AssetCreate(ctx, store.AssetInput{
		Kind: "ssh", Name: "first", OptionsJSON: `{"startupCommand":"printf ok","nested":{"keep":true}}`, Tags: "old",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "second", OptionsJSON: "{}", Tags: "unchanged"})
	if err != nil {
		t.Fatal(err)
	}
	port := int32(2222)
	tags := "web, web，env:prod"
	results, err := db.AssetBulkEdit(ctx, []store.AssetBulkEditTarget{
		{AssetID: first.ID, ExpectedUpdatedAt: first.UpdatedAt},
		{AssetID: second.ID, ExpectedUpdatedAt: second.UpdatedAt + 1},
		{AssetID: "missing", ExpectedUpdatedAt: 1},
	}, store.AssetBulkEditPatch{
		GroupID: store.Value(group.ID), Tags: &tags, JumpAssetID: store.Value(jump.ID), CredentialID: store.Value(credentialID), Port: store.Value(port),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].Err != nil || results[0].Asset == nil {
		t.Fatalf("bulk results = %+v", results)
	}
	if !results[1].Conflict || !errors.Is(results[1].Err, store.ErrWriteConflict) || results[1].Asset != nil {
		t.Fatalf("stale target result = %+v", results[1])
	}
	if results[2].Err == nil || results[2].Asset != nil {
		t.Fatalf("missing target result = %+v", results[2])
	}
	updated := *results[0].Asset
	if updated.GroupID == nil || *updated.GroupID != group.ID || updated.CredID == nil || *updated.CredID != credentialID || updated.Port == nil || *updated.Port != port {
		t.Fatalf("updated asset fields = %+v", updated)
	}
	if updated.Tags != "web, env:prod" {
		t.Fatalf("normalized tags = %q", updated.Tags)
	}
	var options map[string]any
	if err := json.Unmarshal([]byte(updated.OptionsJSON), &options); err != nil {
		t.Fatal(err)
	}
	if options["startupCommand"] != "printf ok" || options["jumpAssetId"] != jump.ID {
		t.Fatalf("updated options = %+v", options)
	}
	if nested, ok := options["nested"].(map[string]any); !ok || nested["keep"] != true {
		t.Fatalf("existing nested options were lost: %+v", options)
	}
	unchanged, err := db.AssetGet(ctx, second.ID)
	if err != nil || unchanged.Tags != "unchanged" || unchanged.GroupID != nil {
		t.Fatalf("conflicted target changed: %+v, err=%v", unchanged, err)
	}

	emptyTags := ""
	cleared, err := db.AssetBulkEdit(ctx, []store.AssetBulkEditTarget{{AssetID: first.ID, ExpectedUpdatedAt: updated.UpdatedAt}}, store.AssetBulkEditPatch{
		GroupID: store.Null[string](), Tags: &emptyTags, JumpAssetID: store.Null[string](), CredentialID: store.Null[string](),
	})
	if err != nil || len(cleared) != 1 || cleared[0].Err != nil || cleared[0].Asset == nil {
		t.Fatalf("clear fields result = %+v, err=%v", cleared, err)
	}
	current := *cleared[0].Asset
	if current.GroupID != nil || current.CredID != nil || current.Tags != "" {
		t.Fatalf("cleared asset = %+v", current)
	}
	options = nil
	if err := json.Unmarshal([]byte(current.OptionsJSON), &options); err != nil {
		t.Fatal(err)
	}
	if _, exists := options["jumpAssetId"]; exists || options["startupCommand"] != "printf ok" {
		t.Fatalf("cleared jump options = %+v", options)
	}
	conflicted, err := db.AssetBulkEdit(ctx, []store.AssetBulkEditTarget{{AssetID: first.ID, ExpectedUpdatedAt: updated.UpdatedAt}}, store.AssetBulkEditPatch{Tags: &tags})
	if err != nil || len(conflicted) != 1 || !conflicted[0].Conflict {
		t.Fatalf("CAS retry after another write = %+v, err=%v", conflicted, err)
	}

	if _, err := db.AssetBulkEdit(ctx, []store.AssetBulkEditTarget{{AssetID: second.ID, ExpectedUpdatedAt: second.UpdatedAt}}, store.AssetBulkEditPatch{
		CredentialID: store.Value(foreignCredentialID),
	}); err == nil {
		t.Fatal("foreign credential was accepted for bulk edit")
	}
	foreignResults, err := db.AssetBulkEdit(otherCtx, []store.AssetBulkEditTarget{{AssetID: second.ID, ExpectedUpdatedAt: second.UpdatedAt}}, store.AssetBulkEditPatch{Tags: &tags})
	if err != nil || len(foreignResults) != 1 || foreignResults[0].Err == nil {
		t.Fatalf("cross-owner result = %+v, err=%v", foreignResults, err)
	}
	unchanged, err = db.AssetGet(ctx, second.ID)
	if err != nil || unchanged.Tags != "unchanged" {
		t.Fatalf("cross-owner write changed asset: %+v, err=%v", unchanged, err)
	}
	selfResults, err := db.AssetBulkEdit(ctx, []store.AssetBulkEditTarget{{AssetID: first.ID, ExpectedUpdatedAt: current.UpdatedAt}}, store.AssetBulkEditPatch{
		JumpAssetID: store.Value(first.ID),
	})
	if err != nil || len(selfResults) != 1 || selfResults[0].Err == nil {
		t.Fatalf("self jump result = %+v, err=%v", selfResults, err)
	}
	if err := db.AssetDelete(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	deletedResults, err := db.AssetBulkEdit(ctx, []store.AssetBulkEditTarget{{AssetID: second.ID, ExpectedUpdatedAt: second.UpdatedAt}}, store.AssetBulkEditPatch{Tags: &tags})
	if err != nil || len(deletedResults) != 1 || deletedResults[0].Err == nil {
		t.Fatalf("deleted target result = %+v, err=%v", deletedResults, err)
	}
	deleted, err := db.AssetGet(ctx, second.ID)
	if err != nil || deleted.DeletedAt == nil || deleted.Tags != "unchanged" {
		t.Fatalf("deleted asset changed: %+v, err=%v", deleted, err)
	}
}

func TestAssetBulkEditValidationDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	asset, err := db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "validation", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	local, err := db.AssetCreate(ctx, store.AssetInput{Kind: "local", Name: "not-a-jump", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	target := store.AssetBulkEditTarget{AssetID: asset.ID, ExpectedUpdatedAt: asset.UpdatedAt}
	tags := "changed"
	invalidPort := int32(65536)
	for name, call := range map[string]func() error{
		"empty targets": func() error {
			_, err := db.AssetBulkEdit(ctx, nil, store.AssetBulkEditPatch{Tags: &tags})
			return err
		},
		"empty patch": func() error {
			_, err := db.AssetBulkEdit(ctx, []store.AssetBulkEditTarget{target}, store.AssetBulkEditPatch{})
			return err
		},
		"duplicate targets": func() error {
			_, err := db.AssetBulkEdit(ctx, []store.AssetBulkEditTarget{target, target}, store.AssetBulkEditPatch{Tags: &tags})
			return err
		},
		"invalid port": func() error {
			_, err := db.AssetBulkEdit(ctx, []store.AssetBulkEditTarget{target}, store.AssetBulkEditPatch{Port: store.Value(invalidPort)})
			return err
		},
		"invalid jump kind": func() error {
			_, err := db.AssetBulkEdit(ctx, []store.AssetBulkEditTarget{target}, store.AssetBulkEditPatch{JumpAssetID: store.Value(local.ID)})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("invalid bulk edit was accepted")
			}
		})
	}
	unchanged, err := db.AssetGet(ctx, asset.ID)
	if err != nil || unchanged.Tags != "" {
		t.Fatalf("validation changed asset: %+v, err=%v", unchanged, err)
	}
}
