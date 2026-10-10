package guard

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type memorySettings struct {
	mu     sync.Mutex
	values map[string]string
	setErr error
}

func (s *memorySettings) SettingGet(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *memorySettings) SettingSet(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	s.values[key] = value
	return nil
}

func TestPermissionPersistenceBeforeRuntimeUpdate(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	manager, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	settings.setErr = errors.New("disk full")
	if err := manager.Set(context.Background(), Config{Mode: Silent, DangerRules: []string{" Rule ", "rule"}}); err == nil {
		t.Fatal("persist failure ignored")
	}
	if manager.Get().Mode != ReadWrite {
		t.Fatal("runtime changed after persistence failure")
	}
	settings.setErr = nil
	if err := manager.Set(context.Background(), Config{Mode: Silent, DangerRules: []string{" Rule ", "rule"}}); err != nil {
		t.Fatal(err)
	}
	config := manager.Get()
	if config.Mode != Silent || len(config.DangerRules) != 1 || settings.values[PermissionSettingKey] == "" {
		t.Fatalf("config=%+v settings=%+v", config, settings.values)
	}
}

func TestDeviceModeOverrideLifecycle(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	manager, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.DeviceMode("asset-a"); ok {
		t.Fatal("unexpected override before set")
	}
	base := Config{Mode: ReadWrite, DangerRules: []string{"rm -rf /"}}
	if got := manager.WithDeviceMode(base, "asset-a"); got.Mode != ReadWrite {
		t.Fatalf("WithDeviceMode without override = %+v", got)
	}
	if got := manager.WithDeviceMode(base, ""); got.Mode != ReadWrite {
		t.Fatalf("WithDeviceMode with empty device = %+v", got)
	}
	if err := manager.SetDeviceMode(context.Background(), "asset-a", Silent); err != nil {
		t.Fatal(err)
	}
	if mode, ok := manager.DeviceMode("asset-a"); !ok || mode != Silent {
		t.Fatalf("DeviceMode = %v %v", mode, ok)
	}
	if got := manager.WithDeviceMode(base, "asset-a"); got.Mode != Silent || len(got.DangerRules) != 1 {
		t.Fatalf("WithDeviceMode = %+v", got)
	}
	if settings.values[DevicePermissionSettingKey] == "" {
		t.Fatal("override not persisted")
	}

	reloaded, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if mode, ok := reloaded.DeviceMode("asset-a"); !ok || mode != Silent {
		t.Fatalf("reloaded DeviceMode = %v %v", mode, ok)
	}

	settings.setErr = errors.New("disk full")
	if err := reloaded.SetDeviceMode(context.Background(), "asset-a", ReadOnly); err == nil {
		t.Fatal("persist failure ignored")
	}
	if mode, _ := reloaded.DeviceMode("asset-a"); mode != Silent {
		t.Fatalf("runtime changed after persistence failure: %v", mode)
	}
	settings.setErr = nil

	if err := reloaded.SetDeviceMode(context.Background(), "asset-a", Mode("yolo")); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if err := reloaded.SetDeviceMode(context.Background(), "  ", Silent); err == nil {
		t.Fatal("empty device accepted")
	}
	if err := reloaded.SetDeviceMode(context.Background(), "asset-a", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.DeviceMode("asset-a"); ok {
		t.Fatal("override not cleared")
	}
	if err := reloaded.SetDeviceMode(context.Background(), "asset-a", ""); err != nil {
		t.Fatal("clear must be idempotent")
	}
}

func TestDeviceModeDropsInvalidPersistedValues(t *testing.T) {
	settings := &memorySettings{values: map[string]string{
		DevicePermissionSettingKey: `{"asset-a":"silent","asset-b":"yolo"}`,
	}}
	manager, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if mode, ok := manager.DeviceMode("asset-a"); !ok || mode != Silent {
		t.Fatalf("DeviceMode = %v %v", mode, ok)
	}
	if _, ok := manager.DeviceMode("asset-b"); ok {
		t.Fatal("invalid persisted mode kept")
	}
}
