package guard

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

var errTestDiskFull = errors.New("disk full")

// Earlier releases stored a device mode on every mode switch. The first load
// removes those not stricter than the global mode, keeps the stricter ones,
// and never runs again.
func TestFirstLoadPrunesImplicitDeviceModes(t *testing.T) {
	ctx := context.Background()
	settings := &memorySettings{values: map[string]string{
		PermissionSettingKey:                  `{"mode":"silent","dangerRules":[]}`,
		DevicePermissionSettingKey:            `{"looser":"full_access","same":"silent","stricter":"read_write","readonly":"read_only"}`,
		DeviceModeClockSettingKeyForUser(ctx): `{"looser":10,"same":20,"stricter":30,"readonly":40}`,
	}}
	manager, err := NewManager(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"looser", "same"} {
		if mode, ok := mustDeviceMode(t, manager, id); ok {
			t.Fatalf("%s kept as %s", id, mode)
		}
	}
	for id, want := range map[string]Mode{"stricter": ReadWrite, "readonly": ReadOnly} {
		if mode, ok := mustDeviceMode(t, manager, id); !ok || mode != want {
			t.Fatalf("%s = %s %v, want %s", id, mode, ok, want)
		}
	}
	var clocks map[string]int64
	if err := json.Unmarshal([]byte(settings.values[DeviceModeClockSettingKeyForUser(ctx)]), &clocks); err != nil {
		t.Fatal(err)
	}
	if _, ok := clocks["looser"]; ok || clocks["stricter"] != 30 {
		t.Fatalf("clocks = %v", clocks)
	}

	// After the cleanup, a looser override set by the user is explicit and survives reloads.
	if err := manager.SetDeviceMode(ctx, "looser", FullAccess); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewManager(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []*Manager{manager, reopened} {
		if mode, ok := mustDeviceMode(t, current, "looser"); !ok || mode != FullAccess {
			t.Fatalf("explicit override = %s %v", mode, ok)
		}
	}
}

func TestImplicitDeviceModePruneRecordsSyncTombstones(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "u-1")
	settings := &grantSyncSettings{memorySettings: &memorySettings{values: map[string]string{
		DevicePermissionSettingKeyForUser(ctx): `{"asset-a":"silent","asset-b":"read_only"}`,
	}}, optIn: true}
	if _, err := NewManager(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if len(settings.tombstones) != 1 || settings.tombstones[0] != AIGrantSyncModePrefix+"asset-a" {
		t.Fatalf("tombstones = %v", settings.tombstones)
	}
}

func TestImplicitDeviceModePruneRetriesAfterWriteFailure(t *testing.T) {
	ctx := context.Background()
	settings := &memorySettings{values: map[string]string{
		DevicePermissionSettingKey: `{"asset-a":"silent"}`,
	}, setErr: errTestDiskFull}
	manager, err := NewManager(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if mode, ok := mustDeviceMode(t, manager, "asset-a"); !ok || mode != Silent {
		t.Fatalf("a failed cleanup changed the loaded modes: %s %v", mode, ok)
	}
	settings.setErr = nil
	if err := manager.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustDeviceMode(t, manager, "asset-a"); ok {
		t.Fatal("cleanup not retried after the store recovered")
	}
}
