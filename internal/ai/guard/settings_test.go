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
