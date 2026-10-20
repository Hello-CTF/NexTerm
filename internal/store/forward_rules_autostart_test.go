package store_test

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestForwardRuleAutoStartPersistsDatabase(t *testing.T) {
	db := openForwardRuleDatabase(t)
	ctx := ipc.WithUserID(context.Background(), "forward-autostart-owner")
	asset := createForwardRuleAsset(t, db, ctx, "autostart-host")

	created, err := db.ForwardRuleSave(ctx, store.ForwardRuleInput{
		AssetID: asset.ID, Name: "auto", ListenPort: 23306,
		TargetHost: "db.internal", TargetPort: 5432, AutoStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created.AutoStart {
		t.Fatal("created rule lost autoStart")
	}
	fetched, err := db.ForwardRuleGet(ctx, created.ID)
	if err != nil || !fetched.AutoStart {
		t.Fatalf("fetched rule = %+v, %v", fetched, err)
	}
	rules, err := db.ForwardRuleList(ctx)
	if err != nil || len(rules) != 1 || !rules[0].AutoStart {
		t.Fatalf("listed rules = %+v, %v", rules, err)
	}

	updated, err := db.ForwardRuleSave(ctx, store.ForwardRuleInput{
		ID: created.ID, AssetID: asset.ID, Name: "auto", ListenPort: 23306,
		TargetHost: "db.internal", TargetPort: 5432, AutoStart: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AutoStart {
		t.Fatal("update did not clear autoStart")
	}
	reupdated, err := db.ForwardRuleSave(ctx, store.ForwardRuleInput{
		ID: created.ID, AssetID: asset.ID, Name: "auto", ListenPort: 23306,
		TargetHost: "db.internal", TargetPort: 5432, AutoStart: true,
	})
	if err != nil || !reupdated.AutoStart {
		t.Fatalf("re-updated rule = %+v, %v", reupdated, err)
	}

	if err := db.ForwardRuleDelete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
}

func TestForwardRuleAssetEnvironmentDatabase(t *testing.T) {
	db := openForwardRuleDatabase(t)
	ctxA := ipc.WithUserID(context.Background(), "forward-env-owner-a")
	ctxB := ipc.WithUserID(context.Background(), "forward-env-owner-b")
	plain := createForwardRuleAsset(t, db, ctxA, "plain-host")
	production, err := db.AssetCreate(ctxA, store.AssetInput{Kind: "ssh", Name: "prod-host", OptionsJSON: "{}", Tags: "linux, Production"})
	if err != nil {
		t.Fatal(err)
	}
	development, err := db.AssetCreate(ctxA, store.AssetInput{Kind: "ssh", Name: "dev-host", OptionsJSON: "{}", Tags: "env:dev"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := createForwardRuleAsset(t, db, ctxB, "foreign-host")
	deleted := createForwardRuleAsset(t, db, ctxA, "deleted-host")
	if err := db.AssetDelete(ctxA, deleted.ID); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		assetID string
		want    string
		code    ipc.Code
	}{
		{name: "plain asset has no environment", assetID: plain.ID, want: ""},
		{name: "production tag", assetID: production.ID, want: "production"},
		{name: "environment tag", assetID: development.ID, want: "development"},
		{name: "missing asset", assetID: "01JEXAMPLEEXAMPLEEXAMPLEEX", code: ipc.CodeBadParam},
		{name: "foreign asset", assetID: foreign.ID, code: ipc.CodeForbidden},
		{name: "deleted asset", assetID: deleted.ID, code: ipc.CodeBadParam},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment, err := db.ForwardRuleAssetEnvironment(ctxA, test.assetID)
			if test.code != "" {
				requireForwardRuleCode(t, err, test.code)
				return
			}
			if err != nil || environment != test.want {
				t.Fatalf("environment = %q, %v; want %q", environment, err, test.want)
			}
		})
	}
	if _, err := db.ForwardRuleAssetEnvironment(ctxA, "  "); err == nil {
		t.Fatal("blank asset accepted")
	}
}
