package production

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestAIAssetResolversResolveRealAssetAndEnvironment(t *testing.T) {
	fixture := newExecTargetsFixture(t)
	production := fixture.connectAsset(t, fixture.owner, "prod", "env:production")
	staging := fixture.connectAsset(t, fixture.owner, "staging", "env:staging")

	environmentOf := aiAssetEnvironmentResolver(fixture.services)
	environment, err := environmentOf(fixture.owner, production.ID)
	if err != nil || environment != "production" {
		t.Fatalf("production asset environment = %q err=%v", environment, err)
	}
	environment, err = environmentOf(fixture.owner, staging.ID)
	if err != nil || environment != "staging" {
		t.Fatalf("staging asset environment = %q err=%v", environment, err)
	}

	requiredOf := aiRequiredEnvironmentNames(fixture.services)
	names, err := requiredOf(fixture.owner, production.ID)
	if err != nil || len(names) != 1 || names[0] != "production" {
		t.Fatalf("production required names = %v err=%v", names, err)
	}
	names, err = requiredOf(fixture.owner, staging.ID)
	if err != nil || names != nil {
		t.Fatalf("staging required names = %v err=%v", names, err)
	}
	// The request owner's configured list extends the classification.
	if err := guard.SetProductionEnvironments(fixture.owner, fixture.database, []string{"staging"}); err != nil {
		t.Fatal(err)
	}
	names, err = requiredOf(fixture.owner, staging.ID)
	if err != nil || len(names) != 1 || names[0] != "staging" {
		t.Fatalf("configured staging required names = %v err=%v", names, err)
	}
	// Another owner's configuration does not leak into this owner's
	// classification, and the other owner cannot resolve the foreign row at
	// all: it fails closed with a visible error instead of silently
	// classifying or degrading.
	if _, err := requiredOf(fixture.other, staging.ID); err == nil {
		t.Fatal("foreign asset resolved without error")
	}
}

func TestAIRunAssetResolverResolvesSessionAsset(t *testing.T) {
	fixture := newExecTargetsFixture(t)
	hostname := "prod.example"
	asset, err := fixture.database.AssetCreate(fixture.owner, store.AssetInput{Kind: session.KindSSH, Name: "prod", Host: &hostname, Tags: "env:production"})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := fixture.services.Sessions.ConnectFor(fixture.owner, session.Asset{ID: asset.ID, Name: asset.Name, Kind: session.KindSSH, StoreBacked: true})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := fixture.database.ConvCreate(fixture.owner, "scoped", map[string]any{"scope": tools.Scope{SessionID: connected.Info().ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.RunInsert(fixture.owner, store.RunRow{ID: "run-1", ConversationID: conversation.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}

	resolve := aiRunAssetResolver(fixture.services)
	resolved, err := resolve(fixture.owner, "run-1")
	if err != nil || resolved != asset.ID {
		t.Fatalf("resolved asset = %q want %q err=%v", resolved, asset.ID, err)
	}
	// An unknown run fails visibly instead of resolving a wrong asset.
	if _, err := resolve(fixture.owner, "run-missing"); err == nil {
		t.Fatal("unknown run resolved")
	}
	// A conversation without a session scope resolves to no asset.
	empty, err := fixture.database.ConvCreate(fixture.owner, "unscoped", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.RunInsert(fixture.owner, store.RunRow{ID: "run-2", ConversationID: empty.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	resolved, err = resolve(fixture.owner, "run-2")
	if err != nil || resolved != "" {
		t.Fatalf("unscoped resolved asset = %q err=%v", resolved, err)
	}
}

func TestAIAssetEnvironmentResolverFailsClosedForDeletedStoreBackedAsset(t *testing.T) {
	fixture := newExecTargetsFixture(t)
	production := fixture.connectAsset(t, fixture.owner, "prod", "env:production")
	if err := fixture.database.AssetDelete(fixture.owner, production.ID); err != nil {
		t.Fatal(err)
	}
	environmentOf := aiAssetEnvironmentResolver(fixture.services)
	if _, err := environmentOf(fixture.owner, production.ID); err == nil {
		t.Fatal("deleted store-backed asset resolved without error")
	}
	requiredOf := aiRequiredEnvironmentNames(fixture.services)
	if _, err := requiredOf(fixture.owner, production.ID); err == nil {
		t.Fatal("deleted store-backed asset produced required names without error")
	}
	// Session memory must not allow a deleted production target: the
	// evaluation fails closed instead of auto-allowing from the remembered rule.
	grants, err := guard.NewGrants(fixture.owner, fixture.database, nil, guard.WithShared(false), guard.WithEnvironmentResolver(aiAssetEnvironmentResolver(fixture.services)))
	if err != nil {
		t.Fatal(err)
	}
	memory := guard.NewMemory()
	memory.AddPathRule("write_file", "/etc/**")
	decision := grants.EvaluateResource(fixture.owner, guard.Config{}, guard.Ruling{Risk: guard.NeedsConfirm}, memory, production.ID, "write_file", "run-1", guard.Resource{Action: "write_file", Path: "/etc/app.conf"})
	if decision.Action != guard.ActionDeny {
		t.Fatalf("deleted production target auto-allowed from session memory: %+v", decision)
	}
}

func TestAIAssetEnvironmentResolverFailsClosedForForeignAsset(t *testing.T) {
	fixture := newExecTargetsFixture(t)
	production := fixture.connectAsset(t, fixture.owner, "prod", "env:production")
	environmentOf := aiAssetEnvironmentResolver(fixture.services)
	if _, err := environmentOf(fixture.other, production.ID); err == nil {
		t.Fatal("foreign asset resolved without error")
	}
	requiredOf := aiRequiredEnvironmentNames(fixture.services)
	if _, err := requiredOf(fixture.other, production.ID); err == nil {
		t.Fatal("foreign asset produced required names without error")
	}
	// Session memory must not allow a foreign production target either.
	grants, err := guard.NewGrants(fixture.other, fixture.database, nil, guard.WithShared(false), guard.WithEnvironmentResolver(aiAssetEnvironmentResolver(fixture.services)))
	if err != nil {
		t.Fatal(err)
	}
	memory := guard.NewMemory()
	memory.AddPathRule("write_file", "/etc/**")
	decision := grants.EvaluateResource(fixture.other, guard.Config{}, guard.Ruling{Risk: guard.NeedsConfirm}, memory, production.ID, "write_file", "run-1", guard.Resource{Action: "write_file", Path: "/etc/app.conf"})
	if decision.Action != guard.ActionDeny {
		t.Fatalf("foreign production target auto-allowed from session memory: %+v", decision)
	}
}

func TestAIAssetEnvironmentResolverDegradesOnlyForProvenSyntheticAssets(t *testing.T) {
	fixture := newExecTargetsFixture(t)
	// A synthetic session-only asset (StoreBacked=false) has no store row:
	// it stays unclassified without failing the evaluation.
	if _, err := fixture.services.Sessions.ConnectFor(fixture.owner, session.Asset{ID: "asset/synthetic", Kind: session.KindSSH}); err != nil {
		t.Fatal(err)
	}
	environmentOf := aiAssetEnvironmentResolver(fixture.services)
	environment, err := environmentOf(fixture.owner, "asset/synthetic")
	if err != nil || environment != "" {
		t.Fatalf("synthetic asset environment = %q err=%v", environment, err)
	}
	requiredOf := aiRequiredEnvironmentNames(fixture.services)
	names, err := requiredOf(fixture.owner, "asset/synthetic")
	if err != nil || names != nil {
		t.Fatalf("synthetic asset required names = %v err=%v", names, err)
	}
	// An asset with no store row and no session evidence cannot be proven
	// synthetic and fails closed.
	if _, err := environmentOf(fixture.owner, "asset/ghost"); err == nil {
		t.Fatal("asset without session evidence resolved without error")
	}
}
