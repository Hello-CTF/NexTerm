package guard

import (
	"context"
	"encoding/json"
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

	// Records written before the marker existed keep their permanent meaning.
	legacy := &memorySettings{values: map[string]string{
		DeviceGrantsSettingKey: `{"device-a":{"deviceId":"device-a","kinds":["terminal_write"],"updatedAt":1}}`,
	}}
	reloaded, err := NewGrants(context.Background(), legacy, nil)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := mustGrant(t, reloaded, "device-a")
	if !ok || !loaded.Permanent {
		t.Fatalf("legacy grant permanence = %+v, %v", loaded, ok)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"permanent":true`) {
		t.Fatalf("legacy grant did not round-trip the marker: %s", encoded)
	}
}
