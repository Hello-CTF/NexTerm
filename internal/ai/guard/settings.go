package guard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

const PermissionSettingKey = "ai.permission"

const DevicePermissionSettingKey = "ai.device_permission"

type Settings interface {
	SettingGet(context.Context, string) (string, bool, error)
	SettingSet(context.Context, string, string) error
}

type Manager struct {
	settings Settings
	mu       sync.RWMutex
	config   Config
	modes    map[string]Mode
}

func NewManager(ctx context.Context, settings Settings) (*Manager, error) {
	manager := &Manager{settings: settings, config: Config{Mode: ReadWrite, DangerRules: []string{}}, modes: make(map[string]Mode)}
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
	raw, found, err = settings.SettingGet(ctx, DevicePermissionSettingKey)
	if err != nil {
		return nil, err
	}
	if found && raw != "" {
		modes := make(map[string]Mode)
		if err := json.Unmarshal([]byte(raw), &modes); err != nil {
			return nil, err
		}
		for deviceID, mode := range modes {
			if !mode.valid() {
				delete(modes, deviceID)
			}
		}
		manager.modes = modes
	}
	return manager, nil
}

func (m *Manager) Snapshot(context.Context) (Config, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Normalized(), nil
}

func (m *Manager) Reload(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, found, err := m.settings.SettingGet(ctx, PermissionSettingKey)
	if err != nil {
		return err
	}
	config := Config{Mode: ReadWrite, DangerRules: []string{}}
	if found && raw != "" {
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			return err
		}
	}
	m.config = config.Normalized()
	return nil
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

func (m Mode) valid() bool {
	return m == ReadOnly || m == ReadWrite || m == Silent || m == Unattended
}

// DeviceMode 返回设备的权限模式覆盖；无覆盖时 ok 为 false。
func (m *Manager) DeviceMode(deviceID string) (Mode, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mode, ok := m.modes[deviceID]
	return mode, ok
}

// WithDeviceMode 在给定配置上应用设备模式覆盖；无覆盖或设备为空时原样返回。
func (m *Manager) WithDeviceMode(config Config, deviceID string) Config {
	if deviceID == "" {
		return config
	}
	if mode, ok := m.DeviceMode(deviceID); ok {
		config.Mode = mode
	}
	return config
}

// SetDeviceMode 设置设备的权限模式覆盖；mode 为空字符串时清除覆盖，回落到全局模式。
func (m *Manager) SetDeviceMode(ctx context.Context, deviceID string, mode Mode) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return errors.New("设备权限模式缺少设备 ID")
	}
	if mode != "" && !mode.valid() {
		return errors.New("权限模式仅支持 read_only/read_write/silent/unattended")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previous, existed := m.modes[deviceID]
	if mode == "" {
		if !existed {
			return nil
		}
		delete(m.modes, deviceID)
	} else {
		m.modes[deviceID] = mode
	}
	encoded, err := json.Marshal(m.modes)
	if err != nil {
		if existed {
			m.modes[deviceID] = previous
		} else {
			delete(m.modes, deviceID)
		}
		return err
	}
	if err := m.settings.SettingSet(ctx, DevicePermissionSettingKey, string(encoded)); err != nil {
		if existed {
			m.modes[deviceID] = previous
		} else {
			delete(m.modes, deviceID)
		}
		return err
	}
	return nil
}
