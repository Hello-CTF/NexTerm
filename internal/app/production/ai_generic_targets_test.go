package production

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestProductionAITargetResolutionOwnerFiltersAndLiveSessions(t *testing.T) {
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	transport := newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		code := 0
		return base.ExecResult{Stdout: "ok", ExitCode: &code}, nil
	})
	var connectorCalls atomic.Int32
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		connectorCalls.Add(1)
		return transport, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	services := &ProductionServices{Store: database, Sessions: manager}

	createAsset := func(ctx context.Context, kind, name, tags string) store.AssetRow {
		t.Helper()
		asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: kind, Name: name, Tags: tags})
		if err != nil {
			t.Fatal(err)
		}
		return asset
	}
	prod := createAsset(ctxA, "ssh", "a-prod", "blue, env:prod")
	staging := createAsset(ctxA, "ssh", "a-staging", "blue, env:staging")
	disconnected := createAsset(ctxA, "ssh", "a-disconnected", "other")
	local := createAsset(ctxA, "local", "a-local", "blue")
	unsupported := createAsset(ctxA, "database", "a-database", "blue")
	foreign := createAsset(ctxB, "ssh", "b-foreign", "blue")

	prodSession, err := manager.ConnectFor(ctxA, session.Asset{ID: prod.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	stagingSession, err := manager.ConnectFor(ctxA, session.Asset{ID: staging.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ConnectFor(ctxB, session.Asset{ID: foreign.ID, Kind: session.KindSSH}); err != nil {
		t.Fatal(err)
	}
	connectedCalls := connectorCalls.Load()

	targets, err := resolveProductionExecTargets(ctxA, services, nil, []string{"BLUE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.SessionIDs) != 2 || !targets.Production {
		t.Fatalf("tag targets = %+v", targets)
	}
	joined := strings.Join(targets.SessionIDs, ",")
	if !strings.Contains(joined, prodSession.ID) || !strings.Contains(joined, stagingSession.ID) {
		t.Fatalf("tag session IDs = %v, want %s and %s", targets.SessionIDs, prodSession.ID, stagingSession.ID)
	}
	explicit, err := resolveProductionExecTargets(ctxA, services, []string{staging.ID}, []string{"blue"})
	if err != nil || len(explicit.SessionIDs) != 1 || explicit.SessionIDs[0] != stagingSession.ID || explicit.Production {
		t.Fatalf("explicit tag intersection = %+v, %v", explicit, err)
	}
	for _, test := range []struct {
		name     string
		ctx      context.Context
		assetIDs []string
		tags     []string
	}{
		{name: "foreign asset", ctx: ctxA, assetIDs: []string{foreign.ID}},
		{name: "tag mismatch", ctx: ctxA, assetIDs: []string{staging.ID}, tags: []string{"other"}},
		{name: "disconnected", ctx: ctxA, tags: []string{"other"}},
		{name: "non SSH explicit", ctx: ctxA, assetIDs: []string{local.ID}},
		{name: "missing owner", ctx: context.Background(), tags: []string{"blue"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveProductionExecTargets(test.ctx, services, test.assetIDs, test.tags); err == nil {
				t.Fatal("target resolution unexpectedly succeeded")
			}
		})
	}
	if got := connectorCalls.Load(); got != connectedCalls {
		t.Fatalf("target resolution auto-connected sessions: before=%d after=%d", connectedCalls, got)
	}

	sessionID, err := resolveProductionAssetSession(ctxA, services, staging.ID)
	if err != nil || sessionID != stagingSession.ID {
		t.Fatalf("asset terminal session = %q, %v; want %q", sessionID, err, stagingSession.ID)
	}
	transport.alive.Store(false)
	if _, err := resolveProductionAssetSession(ctxA, services, staging.ID); err == nil {
		t.Fatal("connected session with unhealthy transport was accepted")
	}
	transport.alive.Store(true)
	for _, test := range []struct {
		name    string
		assetID string
	}{
		{name: "foreign", assetID: foreign.ID},
		{name: "disconnected", assetID: disconnected.ID},
		{name: "unsupported type", assetID: unsupported.ID},
	} {
		t.Run("terminal "+test.name, func(t *testing.T) {
			if _, err := resolveProductionAssetSession(ctxA, services, test.assetID); err == nil {
				t.Fatal("asset terminal resolution unexpectedly succeeded")
			}
		})
	}
	if got := connectorCalls.Load(); got != connectedCalls {
		t.Fatalf("asset terminal resolution auto-connected sessions: before=%d after=%d", connectedCalls, got)
	}
}
