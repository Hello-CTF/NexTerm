package guard

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func environmentGrants(t *testing.T, resolver EnvironmentResolver) *Grants {
	t.Helper()
	grants, err := NewGrants(ipc.WithUserID(context.Background(), "owner-a"), &memorySettings{values: make(map[string]string)}, nil, WithShared(false), WithEnvironmentResolver(resolver))
	if err != nil {
		t.Fatal(err)
	}
	return grants
}

func TestEvaluateResourceFillsProductionFromRealAsset(t *testing.T) {
	grants := environmentGrants(t, func(_ context.Context, assetID string) (string, error) {
		if assetID == "asset-prod" {
			return "production", nil
		}
		return "staging", nil
	})
	memory := NewMemory()
	memory.AddPathRule("write_file", "/etc/**")
	ruling := Ruling{Risk: NeedsConfirm}

	// A production target is never auto-allowed from session memory: the
	// classification comes from the real asset, and the session's remembered
	// rule is ignored, so the call still asks.
	decision := grants.EvaluateResource(ipc.WithUserID(context.Background(), "owner-a"), Config{}, ruling, memory, "asset-prod", "write_file", "run-1", Resource{Action: "write_file", Path: "/etc/app.conf"})
	if decision.Action != ActionAsk {
		t.Fatalf("production target auto-allowed from session memory: %+v", decision)
	}

	// A non-production target keeps the session-memory auto-allow.
	decision = grants.EvaluateResource(ipc.WithUserID(context.Background(), "owner-a"), Config{}, ruling, memory, "asset-stage", "write_file", "run-1", Resource{Action: "write_file", Path: "/etc/app.conf"})
	if decision.Action != ActionAllow {
		t.Fatalf("non-production target not auto-allowed: %+v", decision)
	}
}

func TestEvaluateResourceProductionHonorsConfiguredNames(t *testing.T) {
	settings := &memorySettings{values: map[string]string{}}
	if err := SetProductionEnvironments(ipc.WithUserID(context.Background(), "owner-a"), settings, []string{"Staging"}); err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrants(ipc.WithUserID(context.Background(), "owner-a"), settings, nil, WithShared(false), WithEnvironmentResolver(func(_ context.Context, assetID string) (string, error) {
		return "staging", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	memory := NewMemory()
	memory.AddPathRule("write_file", "/etc/**")
	decision := grants.EvaluateResource(ipc.WithUserID(context.Background(), "owner-a"), Config{}, Ruling{Risk: NeedsConfirm}, memory, "asset-1", "write_file", "run-1", Resource{Action: "write_file", Path: "/etc/app.conf"})
	if decision.Action != ActionAsk {
		t.Fatalf("configured production target auto-allowed from session memory: %+v", decision)
	}
}

func TestEvaluateResourceResolverFailureDenies(t *testing.T) {
	grants := environmentGrants(t, func(_ context.Context, assetID string) (string, error) {
		return "", errors.New("asset store down")
	})
	memory := NewMemory()
	memory.AddPathRule("write_file", "/etc/**")
	decision := grants.EvaluateResource(ipc.WithUserID(context.Background(), "owner-a"), Config{}, Ruling{Risk: NeedsConfirm}, memory, "asset-1", "write_file", "run-1", Resource{Action: "write_file", Path: "/etc/app.conf"})
	if decision.Action != ActionDeny {
		t.Fatalf("resolver failure did not fail closed: %+v", decision)
	}
}

func TestEvaluateResourceWithoutResolverKeepsBuiltInNames(t *testing.T) {
	grants := environmentGrants(t, nil)
	memory := NewMemory()
	memory.AddPathRule("write_file", "/etc/**")
	// Without a resolver the resource stays unclassified, so the built-in
	// path cannot misclassify, and the remembered rule still applies.
	decision := grants.EvaluateResource(ipc.WithUserID(context.Background(), "owner-a"), Config{}, Ruling{Risk: NeedsConfirm}, memory, "asset-1", "write_file", "run-1", Resource{Action: "write_file", Path: "/etc/app.conf"})
	if decision.Action != ActionAllow {
		t.Fatalf("unclassified target not auto-allowed: %+v", decision)
	}
}
