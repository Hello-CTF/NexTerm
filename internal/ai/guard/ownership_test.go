package guard

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestPermissionSettingsAreScopedToUser(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	ctxA := ipc.WithUserID(context.Background(), "user-a")
	ctxB := ipc.WithUserID(context.Background(), "user-b")
	managerA, err := NewManager(ctxA, settings)
	if err != nil {
		t.Fatal(err)
	}
	managerB, err := NewManager(ctxB, settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := managerA.Set(ctxA, Config{Mode: Silent}); err != nil {
		t.Fatal(err)
	}
	if err := managerB.Set(ctxB, Config{Mode: ReadOnly}); err != nil {
		t.Fatal(err)
	}
	if mustPermission(t, managerA, ctxA).Mode != Silent || mustPermission(t, managerB, ctxB).Mode != ReadOnly {
		t.Fatalf("permission modes = %v/%v", mustPermission(t, managerA, ctxA).Mode, mustPermission(t, managerB, ctxB).Mode)
	}
	if err := managerA.SetDeviceMode(ctxA, "asset-a", Unattended); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustDeviceMode(t, managerB, "asset-a", ctxB); ok {
		t.Fatal("user B inherited user A's device override")
	}
	if settings.values[PermissionSettingKey] != "" || settings.values[DevicePermissionSettingKey] != "" {
		t.Fatal("account-scoped permission was written to a global key")
	}
	if settings.values[PermissionSettingKeyForUser(ctxA)] == "" || settings.values[PermissionSettingKeyForUser(ctxB)] == "" {
		t.Fatal("permission was not persisted for both users")
	}
	if settings.values[DevicePermissionSettingKeyForUser(ctxA)] == "" {
		t.Fatal("device override was not persisted for user A")
	}
}
