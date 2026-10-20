package production

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type execTargetsFixture struct {
	services *ProductionServices
	database *store.Store
	owner    context.Context
	other    context.Context
}

func newExecTargetsFixture(t *testing.T) *execTargetsFixture {
	t.Helper()
	owner := ipc.WithUserID(t.Context(), "owner-a")
	other := ipc.WithUserID(t.Context(), "owner-b")
	database, err := store.OpenInMemory(owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	connector := &broadcastGateConnector{transports: make(map[string]*broadcastGateTransport)}
	manager := session.NewManager(session.Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	services := &ProductionServices{Store: database, Sessions: manager, desktop: false}
	return &execTargetsFixture{services: services, database: database, owner: owner, other: other}
}

func (f *execTargetsFixture) connectAsset(t *testing.T, ctx context.Context, name, tags string) store.AssetRow {
	t.Helper()
	hostname := name + ".example"
	row, err := f.database.AssetCreate(ctx, store.AssetInput{Kind: session.KindSSH, Name: name, Host: &hostname, Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.services.Sessions.ConnectFor(ctx, session.Asset{ID: row.ID, Name: row.Name, Kind: session.KindSSH, StoreBacked: true}); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestResolveProductionExecTargetsUsesConfiguredEnvironmentList(t *testing.T) {
	fixture := newExecTargetsFixture(t)
	live := fixture.connectAsset(t, fixture.owner, "live", "env:live")
	production := fixture.connectAsset(t, fixture.owner, "prod", "env:production")
	staging := fixture.connectAsset(t, fixture.owner, "staging", "env:staging")

	// Built-in production names are always classified as production.
	targets, err := resolveProductionExecTargets(fixture.owner, fixture.services, []string{production.ID}, nil)
	if err != nil || !targets.Production {
		t.Fatalf("built-in production targets = %+v, %v", targets, err)
	}
	// Custom environment names are not production without the configuration.
	targets, err = resolveProductionExecTargets(fixture.owner, fixture.services, []string{live.ID}, nil)
	if err != nil || targets.Production {
		t.Fatalf("unconfigured live targets = %+v, %v", targets, err)
	}
	if err := guard.SetProductionEnvironments(fixture.owner, fixture.database, []string{"live"}); err != nil {
		t.Fatal(err)
	}
	targets, err = resolveProductionExecTargets(fixture.owner, fixture.services, []string{live.ID}, nil)
	if err != nil || !targets.Production {
		t.Fatalf("configured live targets = %+v, %v", targets, err)
	}
	// The configuration is per owner: another owner's own live asset is not flagged.
	otherLive := fixture.connectAsset(t, fixture.other, "other-live", "env:live")
	targets, err = resolveProductionExecTargets(fixture.other, fixture.services, []string{otherLive.ID}, nil)
	if err != nil || targets.Production {
		t.Fatalf("cross-owner live targets = %+v, %v", targets, err)
	}
	// Non-production environments stay unflagged for the configured owner.
	targets, err = resolveProductionExecTargets(fixture.owner, fixture.services, []string{staging.ID}, nil)
	if err != nil || targets.Production {
		t.Fatalf("staging targets = %+v, %v", targets, err)
	}
}
