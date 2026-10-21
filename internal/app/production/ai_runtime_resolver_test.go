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
	// Another owner's configuration does not leak into this owner's classification,
	// and the other owner cannot classify the asset at all: it stays
	// unclassified rather than resolving the foreign row.
	names, err = requiredOf(fixture.other, staging.ID)
	if err != nil || names != nil {
		t.Fatalf("cross-owner required names = %v err=%v", names, err)
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

func TestAIAssetEnvironmentResolverDegradesForSyntheticAssets(t *testing.T) {
	fixture := newExecTargetsFixture(t)
	environmentOf := aiAssetEnvironmentResolver(fixture.services)
	// A synthetic session asset that is not a store row has no store
	// environment: it stays unclassified instead of failing the evaluation.
	environment, err := environmentOf(fixture.owner, "asset/synthetic")
	if err != nil || environment != "" {
		t.Fatalf("synthetic asset environment = %q err=%v", environment, err)
	}
	requiredOf := aiRequiredEnvironmentNames(fixture.services)
	names, err := requiredOf(fixture.owner, "asset/synthetic")
	if err != nil || names != nil {
		t.Fatalf("synthetic asset required names = %v err=%v", names, err)
	}
}
