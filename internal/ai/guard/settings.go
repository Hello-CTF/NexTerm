package guard

import (
	"context"
	"encoding/json"
	"sync"
)

const PermissionSettingKey = "ai.permission"

type Settings interface {
	SettingGet(context.Context, string) (string, bool, error)
	SettingSet(context.Context, string, string) error
}

type Manager struct {
	settings Settings
	mu       sync.RWMutex
	config   Config
}

func NewManager(ctx context.Context, settings Settings) (*Manager, error) {
	manager := &Manager{settings: settings, config: Config{Mode: ReadWrite, DangerRules: []string{}}}
	raw, found, err := settings.SettingGet(ctx, PermissionSettingKey)
	if err != nil {
		return nil, err
	}
	if found && raw != "" {
		if err := json.Unmarshal([]byte(raw), &manager.config); err != nil {
			return nil, err
		}
		manager.config = manager.config.Normalized()
	}
	return manager, nil
}

func (m *Manager) Snapshot(context.Context) (Config, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Normalized(), nil
}

func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Normalized()
}

func (m *Manager) Set(ctx context.Context, config Config) error {
	config = config.Normalized()
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.settings.SettingSet(ctx, PermissionSettingKey, string(encoded)); err != nil {
		return err
	}
	m.config = config
	return nil
}
