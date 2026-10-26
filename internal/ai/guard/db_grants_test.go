package guard

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestConnAssetRegistryIsOwnerScoped(t *testing.T) {
	grants, err := NewGrants(context.Background(), &memorySettings{values: make(map[string]string)}, nil, WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	if err := grants.BindConnAsset(ctxA, "conn-1", "asset-a"); err != nil {
		t.Fatal(err)
	}
	assetID, err := grants.ResolveConnAsset(ctxA, "conn-1")
	if err != nil || assetID != "asset-a" {
		t.Fatalf("ResolveConnAsset(owner-a) = %q, %v", assetID, err)
	}
	if _, err := grants.ResolveConnAsset(ctxB, "conn-1"); !errors.Is(err, ErrConnAssetNotFound) {
		t.Fatalf("cross-owner resolve = %v, want %v", err, ErrConnAssetNotFound)
	}
	grants.UnbindConnAsset(ctxA, "conn-1")
	if _, err := grants.ResolveConnAsset(ctxA, "conn-1"); !errors.Is(err, ErrConnAssetNotFound) {
		t.Fatalf("resolve after unbind = %v", err)
	}
	if err := grants.BindConnAsset(context.Background(), "conn-2", "asset-a"); err == nil {
		t.Fatal("ownerless bind used shared state")
	}
	if err := grants.BindConnAsset(ctxA, " ", "asset-a"); err == nil {
		t.Fatal("empty connId accepted")
	}
	if err := grants.BindConnAsset(ctxA, "conn-2", " "); err == nil {
		t.Fatal("empty assetId accepted")
	}
}

func TestEvaluateDBResourceBindsGrantsToTheTunnelAsset(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	grants, err := NewGrants(context.Background(), settings, nil, WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	if _, err := grants.Grant(ctx, "asset-a", []GrantKind{GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	if err := grants.BindConnAsset(ctx, "conn-a", "asset-a"); err != nil {
		t.Fatal(err)
	}
	if err := grants.BindConnAsset(ctx, "conn-b", "asset-b"); err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindDBWrite, "数据库写入需要确认")
	if decision := grants.EvaluateDBResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "conn-a", "db_query", "run-1", Resource{Action: "db_query"}); decision.Action != ActionAllow {
		t.Fatalf("granted asset conn decision = %+v", decision)
	}
	if decision := grants.EvaluateDBResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "conn-b", "db_query", "run-1", Resource{Action: "db_query"}); decision.Action != ActionAsk {
		t.Fatalf("ungranted asset conn decision = %+v", decision)
	}
	if decision := grants.EvaluateDBResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "conn-missing", "db_query", "run-1", Resource{Action: "db_query"}); decision.Action != ActionDeny {
		t.Fatalf("unbound conn decision = %+v", decision)
	}
	other := ipc.WithUserID(context.Background(), "owner-b")
	if decision := grants.EvaluateDBResource(other, Config{Mode: ReadWrite}, ruling, nil, "conn-a", "db_query", "run-1", Resource{Action: "db_query"}); decision.Action != ActionDeny {
		t.Fatalf("cross-owner conn decision = %+v", decision)
	}
}

func TestDBGrantKindsAreExplicit(t *testing.T) {
	for operation, want := range map[string]GrantKind{
		"db_list_tables": GrantKindDBQuery,
		"db_describe":    GrantKindDBQuery,
		"db_query":       GrantKindDBQuery,
		"db_redis_scan":  GrantKindDBQuery,
	} {
		if got := OperationGrantKind(operation); got != want {
			t.Fatalf("OperationGrantKind(%s) = %q, want %q", operation, got, want)
		}
	}
	kinds, err := ParseGrantKinds([]string{"db_query"})
	if err != nil || len(kinds) != 1 || kinds[0] != GrantKindDBQuery {
		t.Fatalf("ParseGrantKinds(db_query) = %v, %v", kinds, err)
	}
}

func TestDeviceGrantPermanenceIsExplicit(t *testing.T) {
	grants, settings := newTestGrants(t, nil)
	grant, err := grants.Grant(context.Background(), "device-a", []GrantKind{GrantKindTerminalWrite})
	if err != nil {
		t.Fatal(err)
	}
	if !grant.Permanent {
		t.Fatal("new device grant is not explicitly permanent")
	}
	raw := settings.values[DeviceGrantsSettingKey]
	if !strings.Contains(raw, `"permanent":true`) {
		t.Fatalf("persisted grant lacks the explicit permanence marker: %s", raw)
	}

	reloaded, err := NewGrants(context.Background(), settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := mustGrant(t, reloaded, "device-a")
	if !ok || !loaded.Permanent || loaded.Legacy {
		t.Fatalf("reloaded grant = %+v, %v", loaded, ok)
	}

	// Records without the marker are pre-1.3.0 data: rejected, not defaulted to permanent.
	legacy := &memorySettings{values: map[string]string{
		DeviceGrantsSettingKey: `{"device-a":{"deviceId":"device-a","kinds":["terminal_write"],"updatedAt":1,"generation":"gen-a"}}`,
	}}
	rejected, err := NewGrants(context.Background(), legacy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mustGrant(t, rejected, "device-a"); ok {
		t.Fatal("grant without the permanence marker was accepted as effective")
	}
	listed := mustGrantList(t, rejected)
	if len(listed) != 1 || !listed[0].Legacy || listed[0].Permanent {
		t.Fatalf("legacy grant listing = %+v", listed)
	}
}

func TestEvaluateDBResourceStripsSessionMemoryForProduction(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	grants, err := NewGrants(context.Background(), settings, nil, WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	if err := grants.BindConnAsset(ctx, "conn-a", "asset-a"); err != nil {
		t.Fatal(err)
	}
	memory := NewMemory()
	memory.Add(KindDBWrite)
	memory.AddPathRule("db_query", "/var/log/**")
	ruling := Confirm(KindDBWrite, "数据库写入需要确认")

	// Without a persistent grant, session memory auto-authorizes ordinary
	// environments but never production targets.
	ordinary := Resource{Action: "db_query", Environment: "staging"}
	if decision := grants.EvaluateDBResource(ctx, Config{Mode: ReadWrite}, ruling, memory, "conn-a", "db_query", "run-1", ordinary); decision.Action != ActionAllow {
		t.Fatalf("ordinary db decision = %+v", decision)
	}
	production := Resource{Action: "db_query", Environment: "production"}
	if decision := grants.EvaluateDBResource(ctx, Config{Mode: ReadWrite}, ruling, memory, "conn-a", "db_query", "run-2", production); decision.Action != ActionAsk {
		t.Fatalf("production db decision = %+v", decision)
	}
	configured := Resource{Action: "db_query", Environment: "live", Production: true}
	if decision := grants.EvaluateDBResource(ctx, Config{Mode: ReadWrite}, ruling, memory, "conn-a", "db_query", "run-3", configured); decision.Action != ActionAsk {
		t.Fatalf("configured production db decision = %+v", decision)
	}

	// An explicit persistent device grant still authorizes production: the
	// session-memory ban does not weaken deliberate long-term grants.
	if _, err := grants.Grant(ctx, "asset-a", []GrantKind{GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	if decision := grants.EvaluateDBResource(ctx, Config{Mode: ReadWrite}, ruling, memory, "conn-a", "db_query", "run-4", production); decision.Action != ActionAllow {
		t.Fatalf("granted production db decision = %+v", decision)
	}
}

func TestEvaluateDBResourceRebindInvalidatesStaleBinding(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	grants, err := NewGrants(context.Background(), settings, nil, WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	if _, err := grants.Grant(ctx, "asset-a", []GrantKind{GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	if err := grants.BindConnAsset(ctx, "conn-a", "asset-a"); err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindDBWrite, "数据库写入需要确认")
	if decision := grants.EvaluateDBResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "conn-a", "db_query", "run-1", Resource{Action: "db_query"}); decision.Action != ActionAllow {
		t.Fatalf("bound decision = %+v", decision)
	}
	// Rebinding the connection to another asset must not inherit the previous
	// asset's grant.
	if err := grants.BindConnAsset(ctx, "conn-a", "asset-b"); err != nil {
		t.Fatal(err)
	}
	if decision := grants.EvaluateDBResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "conn-a", "db_query", "run-2", Resource{Action: "db_query"}); decision.Action != ActionAsk {
		t.Fatalf("stale binding decision = %+v", decision)
	}
	if err := grants.BindConnAsset(ctx, "conn-a", "asset-a"); err != nil {
		t.Fatal(err)
	}
	if decision := grants.EvaluateDBResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "conn-a", "db_query", "run-3", Resource{Action: "db_query"}); decision.Action != ActionAllow {
		t.Fatalf("rebound decision = %+v", decision)
	}
}

func TestEvaluateDBResourceRevokeIsImmediate(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	grants, err := NewGrants(context.Background(), settings, nil, WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	if _, err := grants.Grant(ctx, "asset-a", []GrantKind{GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	if err := grants.BindConnAsset(ctx, "conn-a", "asset-a"); err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindDBWrite, "数据库写入需要确认")
	if decision := grants.EvaluateDBResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "conn-a", "db_query", "run-1", Resource{Action: "db_query"}); decision.Action != ActionAllow {
		t.Fatalf("granted decision = %+v", decision)
	}
	if err := grants.Revoke(ctx, "asset-a"); err != nil {
		t.Fatal(err)
	}
	if decision := grants.EvaluateDBResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "conn-a", "db_query", "run-2", Resource{Action: "db_query"}); decision.Action != ActionAsk {
		t.Fatalf("post-revoke decision = %+v", decision)
	}
}
