package guard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const PermissionSettingKey = "ai.permission"

const DevicePermissionSettingKey = "ai.device_permission"

func PermissionSettingKeyForUser(ctx context.Context) string {
	if userID, ok := ipc.UserIDFromContext(ctx); ok {
		return PermissionSettingKey + ".user." + userID
	}
	return PermissionSettingKey
}

func DevicePermissionSettingKeyForUser(ctx context.Context) string {
	if userID, ok := ipc.UserIDFromContext(ctx); ok {
		return DevicePermissionSettingKey + ".user." + userID
	}
	return DevicePermissionSettingKey
}

type Settings interface {
	SettingGet(context.Context, string) (string, bool, error)
	SettingSet(context.Context, string, string) error
}

type TargetValidator func(context.Context, string) error
type MutationValidator func(context.Context) error

type options struct {
	shared            bool
	targetValidator   TargetValidator
	mutationValidator MutationValidator
}

type Option func(*options)

func WithShared(shared bool) Option {
	return func(options *options) { options.shared = shared }
}

func WithTargetValidator(validator TargetValidator) Option {
	return func(options *options) { options.targetValidator = validator }
}

func WithMutationValidator(validator MutationValidator) Option {
	return func(options *options) { options.mutationValidator = validator }
}

type permissionState struct {
	config Config
	modes  map[string]Mode
}

type Manager struct {
	settings        Settings
	shared          bool
	targetValidator TargetValidator
	mu              sync.RWMutex
	states          map[string]*permissionState
}

func NewManager(ctx context.Context, settings Settings, opts ...Option) (*Manager, error) {
	configuration := options{shared: true}
	for _, apply := range opts {
		apply(&configuration)
	}
	manager := &Manager{settings: settings, shared: configuration.shared, targetValidator: configuration.targetValidator, states: map[string]*permissionState{}}
	if _, err := owner.ID(ctx, manager.shared); err == nil {
		if err := manager.Reload(ctx); err != nil {
			return nil, err
		}
	}
	return manager, nil
}

func loadPermission(ctx context.Context, settings Settings) (permissionState, error) {
	result := permissionState{config: Config{Mode: ReadWrite, DangerRules: []string{}}, modes: make(map[string]Mode)}
	raw, found, err := settings.SettingGet(ctx, PermissionSettingKeyForUser(ctx))
	if err != nil {
		return permissionState{}, err
	}
	if found && raw != "" {
		if err := json.Unmarshal([]byte(raw), &result.config); err != nil {
			return permissionState{}, err
		}
		result.config = result.config.Normalized()
	}
	raw, found, err = settings.SettingGet(ctx, DevicePermissionSettingKeyForUser(ctx))
	if err != nil {
		return permissionState{}, err
	}
	if found && raw != "" {
		if err := json.Unmarshal([]byte(raw), &result.modes); err != nil {
			return permissionState{}, err
		}
		for deviceID, mode := range result.modes {
			if !mode.valid() {
				delete(result.modes, deviceID)
			}
		}
	}
	return result, nil
}

func (m *Manager) stateFor(ctx context.Context) (*permissionState, error) {
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateForLocked(ctx, ownerID)
}

func (m *Manager) stateForLocked(ctx context.Context, ownerID string) (*permissionState, error) {
	if entry, ok := m.states[ownerID]; ok {
		return entry, nil
	}
	entry, err := loadPermission(ctx, m.settings)
	if err != nil {
		return nil, err
	}
	m.states[ownerID] = &entry
	return &entry, nil
}

func (m *Manager) Snapshot(ctx context.Context) (Config, error) {
	entry, err := m.stateFor(ctx)
	if err != nil {
		return Config{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return entry.config.Normalized(), nil
}

func (m *Manager) Reload(ctx context.Context) error {
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := loadPermission(ctx, m.settings)
	if err != nil {
		return err
	}
	m.states[ownerID] = &entry
	return nil
}

func (m *Manager) Get(ctx context.Context) (Config, error) {
	return m.Snapshot(ctx)
}

func (m *Manager) Set(ctx context.Context, config Config) error {
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return err
	}
	config = config.Normalized()
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.stateForLocked(ctx, ownerID)
	if err != nil {
		return err
	}
	if err := m.settings.SettingSet(ctx, PermissionSettingKeyForUser(ctx), string(encoded)); err != nil {
		return err
	}
	entry.config = config
	return nil
}

func (m Mode) valid() bool {
	return m == ReadOnly || m == ReadWrite || m == Silent || m == Unattended
}

func (m *Manager) DeviceMode(ctx context.Context, deviceID string) (Mode, bool, error) {
	entry, err := m.stateFor(ctx)
	if err != nil {
		return "", false, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	mode, ok := entry.modes[deviceID]
	return mode, ok, nil
}

func (m *Manager) WithDeviceMode(ctx context.Context, config Config, deviceID string) (Config, error) {
	if deviceID == "" {
		return config, nil
	}
	mode, ok, err := m.DeviceMode(ctx, deviceID)
	if err != nil {
		return Config{}, err
	}
	if ok {
		if m.targetValidator != nil {
			if err := m.targetValidator(ctx, deviceID); err != nil {
				return Config{}, err
			}
		}
		config.Mode = mode
	}
	return config, nil
}

func (m *Manager) SetDeviceMode(ctx context.Context, deviceID string, mode Mode) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return errors.New("设备权限模式缺少设备 ID")
	}
	if mode != "" && !mode.valid() {
		return errors.New("权限模式仅支持 read_only/read_write/silent/unattended")
	}
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return err
	}
	if mode != "" && m.targetValidator != nil {
		if err := m.targetValidator(ctx, deviceID); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.stateForLocked(ctx, ownerID)
	if err != nil {
		return err
	}
	_, existed := entry.modes[deviceID]
	next := make(map[string]Mode, len(entry.modes)+1)
	for id, value := range entry.modes {
		next[id] = value
	}
	if mode == "" {
		if !existed {
			return nil
		}
		delete(next, deviceID)
	} else {
		next[deviceID] = mode
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := m.settings.SettingSet(ctx, DevicePermissionSettingKeyForUser(ctx), string(encoded)); err != nil {
		return err
	}
	entry.modes = next
	if mode == "" {
		if err := recordAIGrantTombstone(ctx, m.settings, AIGrantSyncModePrefix+deviceID); err != nil {
			return err
		}
	}
	return nil
}
