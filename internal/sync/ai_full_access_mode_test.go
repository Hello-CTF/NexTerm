package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

// Full access syncs like the other modes, as the global mode and as a device mode.
func TestApplyAcceptsFullAccessMode(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	global := ApplyObject{ID: "global", Kind: KindAIPermission, Payload: applyPayload(t, aiPermissionObject{ID: "global", Mode: string(guard.FullAccess), DangerRules: []string{}, UpdatedAt: 100})}
	device := ApplyObject{ID: "mode/dev-1", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantModeObject{ID: "mode/dev-1", DeviceID: "dev-1", Mode: string(guard.FullAccess), UpdatedAt: 100})}
	for _, object := range []ApplyObject{global, device} {
		requireApplyResult(t, mustApplyObjects(t, instance, object).Objects[0], ApplyResultApplied)
		requireApplyResult(t, mustApplyObjects(t, instance, object).Objects[0], ApplyResultIdentical)
	}
	manager, err := guard.NewManager(ctx, instance.db)
	if err != nil {
		t.Fatal(err)
	}
	config, err := manager.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != guard.FullAccess {
		t.Fatalf("global mode = %s", config.Mode)
	}
	if mode, ok := deviceModeForTest(t, ctx, manager, "dev-1"); !ok || mode != guard.FullAccess {
		t.Fatalf("device mode = %s %v", mode, ok)
	}
}
