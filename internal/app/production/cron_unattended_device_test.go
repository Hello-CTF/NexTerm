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
