package production

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestCronUnattendedSurvivesSilentDeviceMode(t *testing.T) {
	ctx := context.Background()
	services, database, _, _ := composeOutcomeRuntime(t, "echo hi")
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "silent-host"})
	if err != nil {
		t.Fatal(err)
	}
	if err := services.Guard.SetDeviceMode(ctx, asset.ID, guard.Silent); err != nil {
		t.Fatal(err)
	}
	cronCtx := guard.WithUnattended(ctx)
	config, err := unattendedPermissions(services.Guard)(cronCtx)
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != guard.Unattended {
		t.Fatalf("unattended permissions mode = %s", config.Mode)
	}
	config, err = services.Guard.WithDeviceMode(cronCtx, config, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != guard.Unattended {
		t.Fatalf("device mode relaxed the cron run to %s", config.Mode)
	}
	decision := guard.Decide(config, guard.Confirm(guard.KindWriteFS, "write"), guard.NewMemory())
	if decision.Action != guard.ActionDeny {
		t.Fatalf("needs_confirm under cron = %+v", decision)
	}
}

// Full access is never available to a scheduled run: the stored global mode and
// the per-device mode both resolve to Unattended under the cron marker.
func TestCronUnattendedSurvivesFullAccessGlobalAndDeviceMode(t *testing.T) {
	ctx := context.Background()
	services, database, _, _ := composeOutcomeRuntime(t, "echo hi")
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "full-access-host"})
	if err != nil {
		t.Fatal(err)
	}
	if err := services.Guard.Set(ctx, guard.Config{Mode: guard.FullAccess}); err != nil {
		t.Fatal(err)
	}
	if err := services.Guard.SetDeviceMode(ctx, asset.ID, guard.FullAccess); err != nil {
		t.Fatal(err)
	}
	interactive, err := unattendedPermissions(services.Guard)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if interactive.Mode != guard.FullAccess {
		t.Fatalf("interactive global mode = %s", interactive.Mode)
	}
	cronCtx := guard.WithUnattended(ctx)
	config, err := unattendedPermissions(services.Guard)(cronCtx)
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != guard.Unattended {
		t.Fatalf("cron global mode = %s", config.Mode)
	}
	resolved, err := services.Guard.WithDeviceMode(cronCtx, config, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Mode != guard.Unattended {
		t.Fatalf("device mode relaxed the cron run to %s", resolved.Mode)
	}
	for _, ruling := range []guard.Ruling{guard.Confirm(guard.KindWriteFS, "write"), guard.Indeterminate("unknown"), guard.Dangerous("danger")} {
		if decision := guard.Decide(resolved, ruling, guard.NewMemory()); decision.Action != guard.ActionDeny {
			t.Fatalf("%s under cron = %+v", ruling.Risk, decision)
		}
	}
}
