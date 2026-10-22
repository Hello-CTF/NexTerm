package guard

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestBuiltInProductionEnvironmentsAlwaysGuarded(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	ctx := context.Background()
	for _, environment := range []string{"production", "prod", "Production", " prod "} {
		production, err := IsProductionEnvironment(ctx, settings, environment)
		if err != nil || !production {
			t.Fatalf("IsProductionEnvironment(%q) = %v, %v", environment, production, err)
		}
	}
	for _, environment := range []string{"", "  ", "staging", "development", "live"} {
		production, err := IsProductionEnvironment(ctx, settings, environment)
		if err != nil || production {
			t.Fatalf("IsProductionEnvironment(%q) = %v, %v", environment, production, err)
		}
	}
}

func TestConfiguredProductionEnvironmentsExtendPerUser(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	ctxA := ipc.WithUserID(context.Background(), "user-a")
	ctxB := ipc.WithUserID(context.Background(), "user-b")
	if err := SetProductionEnvironments(ctxA, settings, []string{" Live ", "LIVE", " staging "}); err != nil {
		t.Fatal(err)
	}
	configured, err := ConfiguredProductionEnvironments(ctxA, settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(configured) != 2 || configured[0] != "live" || configured[1] != "staging" {
		t.Fatalf("configured = %v", configured)
	}
	for _, environment := range []string{"live", "Live", "staging"} {
		production, err := IsProductionEnvironment(ctxA, settings, environment)
		if err != nil || !production {
			t.Fatalf("user A IsProductionEnvironment(%q) = %v, %v", environment, production, err)
		}
	}
	if production, err := IsProductionEnvironment(ctxB, settings, "live"); err != nil || production {
		t.Fatalf("user B inherited user A configuration: %v, %v", production, err)
	}
	if production, err := IsProductionEnvironment(ctxA, settings, "production"); err != nil || !production {
		t.Fatalf("built-in lost after configuration: %v, %v", production, err)
	}
	if settings.values[ProductionEnvironmentsSettingKey] != "" {
		t.Fatal("account-scoped configuration was written to a global key")
	}
}

func TestProductionEnvironmentConfigurationErrors(t *testing.T) {
	settings := &memorySettings{values: map[string]string{
		ProductionEnvironmentsSettingKey: "{",
	}}
	if _, err := ConfiguredProductionEnvironments(context.Background(), settings); err == nil {
		t.Fatal("corrupt configuration accepted")
	}
	if _, err := IsProductionEnvironment(context.Background(), settings, "live"); err == nil {
		t.Fatal("corrupt configuration ignored on classification")
	}
	production, err := IsProductionEnvironment(context.Background(), nil, "production")
	if err != nil || !production {
		t.Fatalf("nil settings must degrade to built-ins: %v, %v", production, err)
	}
	if production, err := IsProductionEnvironment(context.Background(), nil, "live"); err != nil || production {
		t.Fatalf("nil settings must not invent configuration: %v, %v", production, err)
	}
}

func TestNormalizeProductionEnvironments(t *testing.T) {
	normalized := NormalizeProductionEnvironments([]string{" Live ", "", "LIVE", "prod-cluster"})
	if len(normalized) != 2 || normalized[0] != "live" || normalized[1] != "prod-cluster" {
		t.Fatalf("normalized = %v", normalized)
	}
	if got := NormalizeProductionEnvironments(nil); len(got) != 0 {
		t.Fatalf("normalized nil = %v", got)
	}
}
