package production

import (
	"context"
	"sync"
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
	resolved, err := resolve(fixture.owner, "run-1", "exec_commands")
	if err != nil || resolved != asset.ID {
		t.Fatalf("resolved asset = %q want %q err=%v", resolved, asset.ID, err)
	}
	// An unknown run fails visibly instead of resolving a wrong asset.
	if _, err := resolve(fixture.owner, "run-missing", "exec_commands"); err == nil {
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
	resolved, err = resolve(fixture.owner, "run-2", "exec_commands")
	if err != nil || resolved != "" {
		t.Fatalf("unscoped resolved asset = %q err=%v", resolved, err)
	}
}

// fakeScopeConnIdentity is a mutable conn→asset identity map used to drive
// resolveScopeToolAsset through connect, rebind, and disconnect lifecycles.
type fakeScopeConnIdentity struct {
	mu     sync.Mutex
	assets map[string]string
}

func (f *fakeScopeConnIdentity) AssetIDOf(_ context.Context, connID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	assetID, ok := f.assets[connID]
	return assetID, ok
}

func (f *fakeScopeConnIdentity) bind(connID, assetID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assets[connID] = assetID
}

func (f *fakeScopeConnIdentity) drop(connID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.assets, connID)
}

// A mixed terminal+database scope is a supported input: the asset evidence of
// a confirmation must follow the actual tool — a database call binds the
// connection's real DB asset (and its production environment challenge), a
// terminal call in the same scope keeps the session asset, and a rebound or
// vanished connection changes the evidence instead of silently inheriting
// either side's identity.
func TestResolveScopeToolAssetMixedScopePinsToolIdentity(t *testing.T) {
	fixture := newExecTargetsFixture(t)
	hostname := "terminal-a.example"
	assetA, err := fixture.database.AssetCreate(fixture.owner, store.AssetInput{Kind: session.KindSSH, Name: "terminal-a", Host: &hostname})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := fixture.services.Sessions.ConnectFor(fixture.owner, session.Asset{ID: assetA.ID, Name: assetA.Name, Kind: session.KindSSH, StoreBacked: true})
	if err != nil {
		t.Fatal(err)
	}
	dbHost := "db-b.example"
	assetB, err := fixture.database.AssetCreate(fixture.owner, store.AssetInput{Kind: "postgres", Name: "db-b", Host: &dbHost, Tags: "env:production"})
	if err != nil {
		t.Fatal(err)
	}
	scope := tools.Scope{SessionID: connected.Info().ID, ConnID: "conn-1"}
	identity := &fakeScopeConnIdentity{assets: map[string]string{"conn-1": assetB.ID}}

	resolved, err := resolveScopeToolAsset(fixture.owner, identity, fixture.services.Sessions, scope, "db_query")
	if err != nil || resolved != assetB.ID {
		t.Fatalf("db_query evidence = %q want %q err=%v", resolved, assetB.ID, err)
	}
	names, err := aiRequiredEnvironmentNames(fixture.services)(fixture.owner, resolved)
	if err != nil || len(names) != 1 || names[0] != "production" {
		t.Fatalf("db target required environment names = %v err=%v", names, err)
	}

	resolved, err = resolveScopeToolAsset(fixture.owner, identity, fixture.services.Sessions, scope, "send_keys")
	if err != nil || resolved != assetA.ID {
		t.Fatalf("send_keys evidence = %q want %q err=%v", resolved, assetA.ID, err)
	}

	// A rebound connection yields the new asset; a dropped one yields none.
	identity.bind("conn-1", "asset-other")
	resolved, err = resolveScopeToolAsset(fixture.owner, identity, fixture.services.Sessions, scope, "db_query")
	if err != nil || resolved != "asset-other" {
		t.Fatalf("rebound db_query evidence = %q err=%v", resolved, err)
	}
	identity.drop("conn-1")
	resolved, err = resolveScopeToolAsset(fixture.owner, identity, fixture.services.Sessions, scope, "db_query")
	if err != nil || resolved != "" {
		t.Fatalf("dropped connection evidence = %q err=%v", resolved, err)
	}

	// An inline connection never inherits the session asset's identity.
	inline := tools.Scope{SessionID: connected.Info().ID, ConnID: "conn-inline"}
	resolved, err = resolveScopeToolAsset(fixture.owner, identity, fixture.services.Sessions, inline, "db_query")
	if err != nil || resolved != "" {
		t.Fatalf("inline connection evidence = %q err=%v", resolved, err)
	}

	// Without a database identity backend the grant/evidence branch closes.
	resolved, err = resolveScopeToolAsset(fixture.owner, nil, fixture.services.Sessions, scope, "db_query")
	if err != nil || resolved != "" {
		t.Fatalf("backend-less db evidence = %q err=%v", resolved, err)
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
