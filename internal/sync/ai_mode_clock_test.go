package sync

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

// Each device mode carries its own clock: changing one device's mode must not
// make the unrelated entries look newer, or a stale copy could roll back a tightening.
func TestAIGrantModesUsePerDeviceClocks(t *testing.T) {
	device := newTestDevice(t)
	ctx := context.Background()
	putPreferenceSetting(t, device, guard.DevicePermissionSettingKey, `{"dev-1":"read_only","dev-2":"read_write"}`, 900)
	putPreferenceSetting(t, device, guard.DeviceModeClockSettingKeyForUser(ctx), `{"dev-1":300,"dev-2":900}`, 900)

	objects := map[string]localObject{}
	if err := device.engine.collectAIGrantObjects(ctx, objects); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int64{"mode/dev-1": 300, "mode/dev-2": 900} {
		var payload aiGrantModeObject
		if err := json.Unmarshal(objects[id].plaintext, &payload); err != nil || payload.UpdatedAt != want {
			t.Fatalf("%s clock = %d (err %v), want %d", id, payload.UpdatedAt, err, want)
		}
	}

	// A remote loosening older than the local entry clock (but newer than nothing else) loses even though
	// the row revision is far ahead of it; one newer than the entry clock but older than the row wins.
	loosen := func(updatedAt int64) []byte {
		plaintext, err := marshalObject(aiGrantModeObject{ID: "mode/dev-1", DeviceID: "dev-1", Mode: "read_write", UpdatedAt: updatedAt})
		if err != nil {
			t.Fatal(err)
		}
		return plaintext
	}
	if applied, _ := device.engine.applyAIGrantMode(ctx, loosen(200), &SyncReport{}); applied {
		t.Fatal("a mode change older than the entry's clock was applied")
	}
	if applied, _ := device.engine.applyAIGrantMode(ctx, loosen(500), &SyncReport{}); !applied {
		t.Fatal("a mode change newer than the entry's clock was rejected because of another entry's revision")
	}
	if mode, ok := deviceModeForTest(t, ctx, deviceManager(t, device, ctx), "dev-1"); !ok || mode != guard.ReadWrite {
		t.Fatalf("dev-1 mode = %q, %v", mode, ok)
	}
	if mode, _ := deviceModeForTest(t, ctx, deviceManager(t, device, ctx), "dev-2"); mode != guard.ReadWrite {
		t.Fatalf("dev-2 mode changed to %q", mode)
	}
}

// Setting a mode through the guard stamps that device's clock only.
func TestGuardSetDeviceModeStampsOnlyThatDevice(t *testing.T) {
	device := newTestDevice(t)
	ctx := context.Background()
	manager := deviceManager(t, device, ctx)
	if err := manager.SetDeviceMode(ctx, "dev-1", guard.ReadOnly); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDeviceMode(ctx, "dev-2", guard.ReadWrite); err != nil {
		t.Fatal(err)
	}
	raw, _, found := readPreferenceSetting(t, device, guard.DeviceModeClockSettingKeyForUser(ctx))
	var clocks map[string]int64
	if !found || json.Unmarshal([]byte(raw), &clocks) != nil || clocks["dev-1"] == 0 || clocks["dev-2"] <= clocks["dev-1"]-1 {
		t.Fatalf("clocks = %q (found %v)", raw, found)
	}
	if err := manager.SetDeviceMode(ctx, "dev-1", ""); err != nil {
		t.Fatal(err)
	}
	raw, _, _ = readPreferenceSetting(t, device, guard.DeviceModeClockSettingKeyForUser(ctx))
	clocks = nil
	if json.Unmarshal([]byte(raw), &clocks) != nil || clocks["dev-1"] != 0 || clocks["dev-2"] == 0 {
		t.Fatalf("clocks after clearing dev-1 = %q", raw)
	}
}
