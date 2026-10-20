package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func openForwardRuleDatabase(t *testing.T) *store.Store {
	t.Helper()
	db := dbtest.NewFixture(t).OpenStore(t)
	if err := db.GORM().WithContext(t.Context()).AutoMigrate(&store.ForwardRuleRow{}); err != nil {
		t.Fatalf("migrate forward rules: %v", err)
	}
	if err := db.GORM().WithContext(t.Context()).AutoMigrate(&store.ForwardRuleRow{}); err != nil {
		t.Fatalf("repeat forward rule migration: %v", err)
	}
	return db
}

func requireForwardRuleCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func createForwardRuleAsset(t *testing.T, db *store.Store, ctx context.Context, name string) store.AssetRow {
	t.Helper()
	asset, err := db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: name, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func TestForwardRuleCRUDOwnerAndAssetLifecycleDatabase(t *testing.T) {
	db := openForwardRuleDatabase(t)
	ctxA := ipc.WithUserID(context.Background(), "forward-owner-a")
	ctxB := ipc.WithUserID(context.Background(), "forward-owner-b")
	assetA := createForwardRuleAsset(t, db, ctxA, "a-host")
	assetB := createForwardRuleAsset(t, db, ctxB, "b-host")
	deletedAsset := createForwardRuleAsset(t, db, ctxA, "deleted-host")
	if err := db.AssetDelete(ctxA, deletedAsset.ID); err != nil {
		t.Fatal(err)
	}
	input := store.ForwardRuleInput{
		AssetID: assetA.ID, Name: "  database  ", ListenPort: 13306,
		TargetHost: "  [2001:db8::10]  ", TargetPort: 5432,
	}

	for _, test := range []struct {
		name  string
		input store.ForwardRuleInput
		code  ipc.Code
	}{
		{name: "zero listen port", input: store.ForwardRuleInput{AssetID: assetA.ID, TargetHost: "db", TargetPort: 5432}, code: ipc.CodeBadParam},
		{name: "listen port overflow", input: store.ForwardRuleInput{AssetID: assetA.ID, ListenPort: 65536, TargetHost: "db", TargetPort: 5432}, code: ipc.CodeBadParam},
		{name: "zero target port", input: store.ForwardRuleInput{AssetID: assetA.ID, ListenPort: 13306, TargetHost: "db"}, code: ipc.CodeBadParam},
		{name: "target port overflow", input: store.ForwardRuleInput{AssetID: assetA.ID, ListenPort: 13306, TargetHost: "db", TargetPort: 65536}, code: ipc.CodeBadParam},
		{name: "missing host", input: store.ForwardRuleInput{AssetID: assetA.ID, ListenPort: 13306, TargetPort: 5432}, code: ipc.CodeBadParam},
		{name: "missing asset", input: store.ForwardRuleInput{AssetID: ids.New(), ListenPort: 13306, TargetHost: "db", TargetPort: 5432}, code: ipc.CodeBadParam},
		{name: "deleted asset", input: store.ForwardRuleInput{AssetID: deletedAsset.ID, ListenPort: 13306, TargetHost: "db", TargetPort: 5432}, code: ipc.CodeBadParam},
		{name: "foreign asset", input: store.ForwardRuleInput{AssetID: assetB.ID, ListenPort: 13306, TargetHost: "db", TargetPort: 5432}, code: ipc.CodeForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := db.ForwardRuleSave(ctxA, test.input)
			requireForwardRuleCode(t, err, test.code)
		})
	}

	created, err := db.ForwardRuleSave(ctxA, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.OwnerID != "forward-owner-a" || created.Name != "database" ||
		created.TargetHost != "2001:db8::10" || created.CreatedAt == 0 || created.UpdatedAt == 0 {
		t.Fatalf("created rule = %+v", created)
	}
	if rules, err := db.ForwardRuleList(ctxB); err != nil || len(rules) != 0 {
		t.Fatalf("other owner list = %+v, %v", rules, err)
	}
	if rules, err := db.ForwardRuleList(context.Background()); err != nil || len(rules) != 0 {
		t.Fatalf("ownerless list exposed user rules = %+v, %v", rules, err)
	}
	_, err = db.ForwardRuleGet(ctxB, created.ID)
	requireForwardRuleCode(t, err, ipc.CodeNotFound)
	_, err = db.ForwardRuleSave(ctxB, store.ForwardRuleInput{
		ID: created.ID, AssetID: assetB.ID, ListenPort: 22000, TargetHost: "attacker", TargetPort: 22,
	})
	requireForwardRuleCode(t, err, ipc.CodeNotFound)
	if err := db.ForwardRuleDelete(ctxB, created.ID); err == nil {
		t.Fatal("cross-owner delete succeeded")
	}

	updated, err := db.ForwardRuleSave(ctxA, store.ForwardRuleInput{
		ID: created.ID, AssetID: assetA.ID, Name: "renamed", ListenPort: 15432,
		TargetHost: "db.internal", TargetPort: 5433,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != created.ID || updated.CreatedAt != created.CreatedAt || updated.Name != "renamed" ||
		updated.ListenPort != 15432 || updated.TargetHost != "db.internal" || updated.TargetPort != 5433 {
		t.Fatalf("updated rule = %+v", updated)
	}
	unchanged, err := db.ForwardRuleSave(ctxA, store.ForwardRuleInput{
		ID: created.ID, AssetID: assetA.ID, Name: "renamed", ListenPort: 15432,
		TargetHost: "db.internal", TargetPort: 5433,
	})
	if err != nil || unchanged.ID != created.ID || unchanged.Name != "renamed" {
		t.Fatalf("repeat identical update = %+v, %v", unchanged, err)
	}

	if err := db.AssetDelete(ctxA, assetA.ID); err != nil {
		t.Fatal(err)
	}
	requireForwardRuleCode(t, db.ForwardRuleAssetAvailable(ctxA, assetA.ID), ipc.CodeBadParam)
	_, err = db.ForwardRuleSave(ctxA, store.ForwardRuleInput{
		ID: created.ID, AssetID: assetA.ID, Name: "must-not-save", ListenPort: 15432,
		TargetHost: "db.internal", TargetPort: 5433,
	})
	requireForwardRuleCode(t, err, ipc.CodeBadParam)
	retained, err := db.ForwardRuleGet(ctxA, created.ID)
	if err != nil || retained.Name != "renamed" {
		t.Fatalf("rule after rejected update = %+v, %v", retained, err)
	}
	if err := db.ForwardRuleDelete(ctxA, created.ID); err != nil {
		t.Fatalf("delete rule for deleted asset: %v", err)
	}
	_, err = db.ForwardRuleGet(ctxA, created.ID)
	requireForwardRuleCode(t, err, ipc.CodeNotFound)
}

func TestForwardRulePortConstraintsDatabase(t *testing.T) {
	db := openForwardRuleDatabase(t)
	for _, test := range []struct {
		name       string
		listenPort int32
		targetPort int32
	}{
		{name: "zero listen", listenPort: 0, targetPort: 5432},
		{name: "listen overflow", listenPort: 65536, targetPort: 5432},
		{name: "zero target", listenPort: 15432, targetPort: 0},
		{name: "target overflow", listenPort: 15432, targetPort: 65536},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := store.ForwardRuleRow{
				ID: ids.New(), OwnerID: "schema-owner", AssetID: "schema-asset", Name: "invalid",
				ListenPort: test.listenPort, TargetHost: "db", TargetPort: test.targetPort,
				CreatedAt: 1, UpdatedAt: 1,
			}
			if err := db.GORM().WithContext(t.Context()).Create(&row).Error; err == nil {
				t.Fatal("database accepted an invalid fixed port")
			}
		})
	}
}
