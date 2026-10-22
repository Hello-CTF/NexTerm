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

	// The legacy IPC edit cannot express autoStart; it must not clear the persisted flag.
	updated, err := db.ForwardRuleSave(ctx, store.ForwardRuleInput{
		ID: created.ID, AssetID: asset.ID, Name: "renamed", ListenPort: 23307,
		TargetHost: "db.internal", TargetPort: 5432,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.AutoStart {
		t.Fatal("legacy edit cleared autoStart")
	}
	if updated.Name != "renamed" || updated.ListenPort != 23307 {
		t.Fatalf("legacy edit lost other fields = %+v", updated)
	}

	if err := db.ForwardRuleDelete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
}

func TestForwardRuleSetAutoStartDatabase(t *testing.T) {
	db := openForwardRuleDatabase(t)
	ctxA := ipc.WithUserID(context.Background(), "forward-autostart-owner-a")
	ctxB := ipc.WithUserID(context.Background(), "forward-autostart-owner-b")
	asset := createForwardRuleAsset(t, db, ctxA, "autostart-host")
	created, err := db.ForwardRuleSave(ctxA, store.ForwardRuleInput{
		AssetID: asset.ID, Name: "auto", ListenPort: 23306,
		TargetHost: "db.internal", TargetPort: 5432,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.AutoStart {
		t.Fatal("rule created without autoStart")
	}

	enabled, err := db.ForwardRuleSetAutoStart(ctxA, created.ID, true)
	if err != nil || !enabled.AutoStart {
		t.Fatalf("enable = %+v, %v", enabled, err)
	}
	disabled, err := db.ForwardRuleSetAutoStart(ctxA, created.ID, false)
	if err != nil || disabled.AutoStart {
		t.Fatalf("disable = %+v, %v", disabled, err)
	}

	if _, err := db.ForwardRuleSetAutoStart(ctxA, "  ", true); err == nil {
		t.Fatal("blank id accepted")
	}
	if _, err := db.ForwardRuleSetAutoStart(ctxA, "not-an-id", true); err == nil {
		t.Fatal("malformed id accepted")
	}
	_, err = db.ForwardRuleSetAutoStart(ctxA, "01JEXAMPLEEXAMPLEEXAMPLEEX", true)
	requireForwardRuleCode(t, err, ipc.CodeNotFound)
	_, err = db.ForwardRuleSetAutoStart(ctxB, created.ID, true)
	requireForwardRuleCode(t, err, ipc.CodeNotFound)
	fetched, err := db.ForwardRuleGet(ctxA, created.ID)
	if err != nil || fetched.AutoStart {
		t.Fatalf("rule after rejected toggles = %+v, %v", fetched, err)
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
