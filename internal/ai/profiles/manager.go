package profiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	SettingKey   = store.AIProfilesSettingKey
	StoreVersion = 1
)

var (
	ErrNoActiveProfile       = errors.New("尚未启用 AI 模型档案，请先在设置中选择并启用模型")
	ErrProfileNotFound       = errors.New("AI 模型档案不存在，可能已被删除，请检查设置")
	ErrProfileKeyUnavailable = errors.New("AI 模型档案缺少可用的 API Key，请检查档案的密钥配置")
)

type Settings interface {
	SettingGet(ctx context.Context, key string) (value string, found bool, err error)
	SettingSet(ctx context.Context, key, value string) error
}

type state struct {
	Version  int       `json:"version"`
	Profiles []Profile `json:"profiles"`
	ActiveID *string   `json:"activeId"`
}

type ownerState struct {
	state    state
	circuits map[string]circuitEntry
}

type Manager struct {
	clientOptions []provider.Option
	settings      Settings
	protector     store.SecretProtector
	shared        bool
	mu            sync.RWMutex
	states        map[string]*ownerState
}

type options struct {
	shared        bool
	clientOptions []provider.Option
}

type Option func(*options)

func WithClientOptions(clientOptions ...provider.Option) Option {
	return func(options *options) { options.clientOptions = append(options.clientOptions, clientOptions...) }
}

// WithShared selects the legacy shared state for desktop and explicit open deployments.
func WithShared(shared bool) Option {
	return func(options *options) { options.shared = shared }
}

type circuitEntry struct {
	breaker   *provider.CircuitBreaker
	threshold int
	cooldown  time.Duration
}

type protectorSource interface {
	SecretProtector() store.SecretProtector
}

type unlockListenerSource interface {
	AddUnlockListener(listener func())
}

type loadResult struct {
	state     state
	needsSave bool
}

func NewManager(ctx context.Context, settings Settings, opts ...Option) (*Manager, error) {
	if settings == nil {
		return nil, errors.New("AI profile settings store is nil")
	}
	configuration := options{shared: true}
	for _, apply := range opts {
		apply(&configuration)
	}
	var protector store.SecretProtector
	if source, ok := settings.(protectorSource); ok {
		protector = source.SecretProtector()
	}
	manager := &Manager{settings: settings, protector: protector, shared: configuration.shared, states: map[string]*ownerState{}, clientOptions: configuration.clientOptions}
	if _, err := owner.ID(ctx, manager.shared); err == nil {
		if err := manager.Reload(ctx); err != nil {
			return nil, err
		}
	}
	if listener, ok := protector.(unlockListenerSource); ok {
		listener.AddUnlockListener(manager.reloadOnUnlock)
	}
	return manager, nil
}

func (m *Manager) reloadOnUnlock() {
	if !m.shared {
		return
	}
	if err := m.Reload(owner.WithOpenAccess(context.Background())); err != nil {
		slog.Warn("凭据库解锁后刷新 AI 模型档案失败", "error", err)
	}
}

func (m *Manager) stateFor(ctx context.Context) (string, *ownerState, error) {
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return "", nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.stateForLocked(ctx, ownerID)
	return ownerID, entry, err
}

func (m *Manager) stateForLocked(ctx context.Context, ownerID string) (*ownerState, error) {
	if entry, ok := m.states[ownerID]; ok {
		return entry, nil
	}
	result, err := load(ctx, m.settings, ownerID)
	if err != nil {
		return nil, err
	}
	entry := &ownerState{circuits: map[string]circuitEntry{}}
	if err := m.applyLoadResultLocked(ctx, ownerID, entry, result); err != nil {
		return nil, err
	}
	m.states[ownerID] = entry
	return entry, nil
}

func (m *Manager) applyLoadResultLocked(ctx context.Context, ownerID string, entry *ownerState, result loadResult) error {
	if !result.needsSave {
		entry.state = result.state
		return nil
	}
	saved, err := save(ctx, m.settings, m.protector, ownerID, result.state)
	if err != nil {
		if !isVaultUnavailable(err) && !store.IsBusy(err) {
			return err
		}
		entry.state = result.state
		return nil
	}
	entry.state = saved
	return nil
}

func (m *Manager) Overview(ctx context.Context) (Overview, error) {
	_, entry, err := m.stateFor(ctx)
	if err != nil {
		return Overview{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return entry.state.overview(), nil
}

func (m *Manager) ActiveProfile(ctx context.Context) (Profile, bool, error) {
	_, entry, err := m.stateFor(ctx)
	if err != nil {
		return Profile{}, false, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	profile, ok := entry.state.active()
	if !ok {
		return Profile{}, false, nil
	}
	return cloneProfile(profile), true, nil
}

func (m *Manager) ActiveConfig(ctx context.Context) (provider.Config, bool, error) {
	profile, ok, err := m.ActiveProfile(ctx)
	if err != nil || !ok {
		return provider.Config{}, ok, err
	}
	config := profile.ProviderConfig()
	key, err := m.resolveAPIKey(profile)
	if err != nil {
		config.APIKey = ""
	} else {
		config.APIKey = key
	}
	return config, true, nil
}

func (m *Manager) ActiveClient(ctx context.Context, options ...provider.Option) (*provider.Client, error) {
	ownerID, entry, err := m.stateFor(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	profile, ok := entry.state.active()
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNoActiveProfile
	}
	config := profile.ProviderConfig()
	key, err := m.resolveAPIKey(profile)
	if err != nil {
		return nil, err
	}
	config.APIKey = key
	clientOptions := append(profile.ClientOptions(), m.circuitOption(ownerID, profile))
	clientOptions = append(clientOptions, m.clientOptions...)
	return provider.NewClient(config, append(clientOptions, options...)...)
}

func (m *Manager) circuitOption(ownerID string, profile Profile) provider.Option {
	threshold, cooldown := profile.CircuitConfig()
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.states[ownerID]
	if entry == nil {
		entry = &ownerState{state: emptyState(), circuits: map[string]circuitEntry{}}
		m.states[ownerID] = entry
	}
	if current, ok := entry.circuits[profile.ID]; ok && current.threshold == threshold && current.cooldown == cooldown {
		return provider.WithCircuitBreaker(current.breaker)
	}
	breaker := provider.NewCircuitBreaker(threshold, cooldown)
	entry.circuits[profile.ID] = circuitEntry{breaker: breaker, threshold: threshold, cooldown: cooldown}
	return provider.WithCircuitBreaker(breaker)
}

type CircuitStatus struct {
	ConsecutiveFailures int        `json:"consecutiveFailures"`
	OpenUntil           *time.Time `json:"openUntil,omitempty"`
}

func (m *Manager) CircuitStatus(ctx context.Context, id string) (CircuitStatus, bool, error) {
	_, entry, err := m.stateFor(ctx)
	if err != nil {
		return CircuitStatus{}, false, err
	}
	m.mu.RLock()
	current, ok := entry.circuits[id]
	m.mu.RUnlock()
	if !ok {
		return CircuitStatus{}, false, nil
	}
	snapshot := current.breaker.Snapshot()
	status := CircuitStatus{ConsecutiveFailures: snapshot.ConsecutiveFailures}
	if !snapshot.OpenUntil.IsZero() {
		openUntil := snapshot.OpenUntil
		status.OpenUntil = &openUntil
	}
	return status, true, nil
}

func (m *Manager) resolveAPIKey(profile Profile) (string, error) {
	switch {
	case profile.APIKey == "":
		return "", nil
	case profile.APIKey == MaskedAPIKey:
		return "", fmt.Errorf("%w: %s", ErrProfileKeyUnavailable, profile.Name)
	case m.protector == nil:
		return "", ipc.NewError(ipc.CodeVaultLocked, "凭据库不可用，无法读取 AI 模型档案密钥")
	default:
		plaintext, err := m.protector.DecryptSecret(context.Background(), profile.APIKey)
		if err != nil {
			return "", err
		}
		if plaintext == MaskedAPIKey {
			return "", fmt.Errorf("%w: %s", ErrProfileKeyUnavailable, profile.Name)
		}
		return plaintext, nil
	}
}

func (m *Manager) Profile(ctx context.Context, id string) (Profile, bool, error) {
	_, entry, err := m.stateFor(ctx)
	if err != nil {
		return Profile{}, false, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	profile, ok := entry.state.find(id)
	if !ok {
		return Profile{}, false, nil
	}
	return cloneProfile(profile), true, nil
}

func (m *Manager) ClientFor(ctx context.Context, id string, options ...provider.Option) (*provider.Client, error) {
	if id == "" {
		return m.ActiveClient(ctx, options...)
	}
	ownerID, entry, err := m.stateFor(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	profile, ok := entry.state.find(id)
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProfileNotFound, id)
	}
	config := profile.ProviderConfig()
	key, err := m.resolveAPIKey(profile)
	if err != nil {
		return nil, err
	}
	config.APIKey = key
	clientOptions := append(profile.ClientOptions(), m.circuitOption(ownerID, profile))
	clientOptions = append(clientOptions, m.clientOptions...)
	return provider.NewClient(config, append(clientOptions, options...)...)
}

func (m *Manager) Save(ctx context.Context, profile Profile) (Overview, error) {
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return Overview{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.stateForLocked(ctx, ownerID)
	if err != nil {
		return Overview{}, err
	}
	next := entry.state.clone()
	profile = profile.Normalized()
	if profile.ID == "" {
		profile.ID = ids.New()
	}
	if profile.APIKey == MaskedAPIKey || (m.protector != nil && strings.HasPrefix(profile.APIKey, store.SecretEnvelopePrefix)) {
		if existing, ok := entry.state.find(profile.ID); ok {
			profile.APIKey = existing.APIKey
		} else {
			profile.APIKey = ""
		}
	}
	updated := false
	for index := range next.Profiles {
		if next.Profiles[index].ID == profile.ID {
			next.Profiles[index] = profile
			updated = true
			break
		}
	}
	if !updated {
		next.Profiles = append(next.Profiles, profile)
	}
	next.ensureActive()
	saved, err := save(ctx, m.settings, m.protector, ownerID, next)
	if err != nil {
		return entry.state.overview(), err
	}
	entry.state = saved
	return entry.state.overview(), nil
}

func (m *Manager) Activate(ctx context.Context, id string) (Overview, error) {
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return Overview{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.stateForLocked(ctx, ownerID)
	if err != nil {
		return Overview{}, err
	}
	found := false
	for _, profile := range entry.state.Profiles {
		if profile.ID == id {
			found = true
			break
		}
	}
	if !found {
		return entry.state.overview(), fmt.Errorf("%w: %s", ErrProfileNotFound, id)
	}
	next := entry.state.clone()
	next.ActiveID = cloneString(&id)
	saved, err := save(ctx, m.settings, m.protector, ownerID, next)
	if err != nil {
		return entry.state.overview(), err
	}
	entry.state = saved
	return entry.state.overview(), nil
}

func (m *Manager) Delete(ctx context.Context, id string) (Overview, error) {
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return Overview{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.stateForLocked(ctx, ownerID)
	if err != nil {
		return Overview{}, err
	}
	next := entry.state.clone()
	profiles := make([]Profile, 0, len(next.Profiles))
	for _, profile := range next.Profiles {
		if profile.ID != id {
			profiles = append(profiles, profile)
		}
	}
	if len(profiles) == len(next.Profiles) {
		return entry.state.overview(), nil
	}
	next.Profiles = profiles
	next.ensureActive()
	if deleter, ok := m.settings.(aiProfileSyncDeleter); ok {
		saved, encoded, err := encodeState(ctx, m.protector, next)
		if err != nil {
			return entry.state.overview(), err
		}
		if err := deleter.AIProfilesDeleteTx(ctx, string(encoded), id, ids.NowMS()); err != nil {
			return entry.state.overview(), err
		}
		entry.state = saved
		delete(entry.circuits, id)
		return entry.state.overview(), nil
	}
	saved, err := save(ctx, m.settings, m.protector, ownerID, next)
	if err != nil {
		return entry.state.overview(), err
	}
	entry.state = saved
	delete(entry.circuits, id)
	if err := m.recordDeleteTombstone(ctx, id); err != nil {
		return entry.state.overview(), err
	}
	return entry.state.overview(), nil
}

func (m *Manager) Reload(ctx context.Context) error {
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result, err := load(ctx, m.settings, ownerID)
	if err != nil {
		return err
	}
	entry := m.states[ownerID]
	if entry == nil {
		entry = &ownerState{circuits: map[string]circuitEntry{}}
		m.states[ownerID] = entry
	}
	return m.applyLoadResultLocked(ctx, ownerID, entry, result)
}

func load(ctx context.Context, settings Settings, ownerID string) (loadResult, error) {
	raw, found, err := settings.SettingGet(ctx, store.AIProfilesSettingKeyForUser(ownerID))
	if err != nil {
		return loadResult{}, err
	}
	if !found {
		return loadResult{state: emptyState()}, nil
	}
	var persisted state
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		slog.Warn("AI 模型档案数据损坏，已隔离并替换为空状态", "error", err)
		return loadResult{state: state{Version: StoreVersion, Profiles: []Profile{}}, needsSave: true}, nil
	}
	if persisted.Version <= 0 || persisted.Version > StoreVersion {
		return loadResult{}, fmt.Errorf("unsupported AI profile store version %d", persisted.Version)
	}
	before, _ := json.Marshal(persisted)
	persisted = persisted.normalized()
	after, _ := json.Marshal(persisted)
	return loadResult{state: persisted, needsSave: !bytes.Equal(before, after)}, nil
}

func save(ctx context.Context, settings Settings, protector store.SecretProtector, ownerID string, value state) (state, error) {
	saved, encoded, err := encodeState(ctx, protector, value)
	if err != nil {
		return value, err
	}
	if err := settings.SettingSet(ctx, store.AIProfilesSettingKeyForUser(ownerID), string(encoded)); err != nil {
		return value, err
	}
	return saved, nil
}

func encodeState(ctx context.Context, protector store.SecretProtector, value state) (state, []byte, error) {
	value = value.normalized()
	if protector != nil {
		for index := range value.Profiles {
			profile := &value.Profiles[index]
			if profile.APIKey == "" || strings.HasPrefix(profile.APIKey, store.SecretEnvelopePrefix) {
				continue
			}
			envelope, err := protector.EncryptSecret(ctx, profile.APIKey)
			if err != nil {
				return value, nil, err
			}
			profile.APIKey = envelope
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return value, nil, fmt.Errorf("encode AI profiles: %w", err)
	}
	return value, encoded, nil
}

func isVaultUnavailable(err error) bool {
	var appErr *ipc.Error
	return errors.As(err, &appErr) && (appErr.Code == ipc.CodeVaultLocked || appErr.Code == ipc.CodeVaultNotInit)
}

func emptyState() state {
	return state{Version: StoreVersion, Profiles: []Profile{}}
}

func (s state) normalized() state {
	result := state{Version: StoreVersion, Profiles: make([]Profile, 0, len(s.Profiles)), ActiveID: cloneString(s.ActiveID)}
	seen := make(map[string]struct{}, len(s.Profiles))
	for _, candidate := range s.Profiles {
		profile := candidate.Normalized()
		if profile.ID == "" {
			profile.ID = ids.New()
		}
		if _, exists := seen[profile.ID]; exists {
			continue
		}
		seen[profile.ID] = struct{}{}
		result.Profiles = append(result.Profiles, profile)
	}
	result.ensureActive()
	return result
}

func (s *state) ensureActive() {
	if s.ActiveID != nil {
		for _, profile := range s.Profiles {
			if profile.ID == *s.ActiveID {
				return
			}
		}
	}
	if len(s.Profiles) == 0 {
		s.ActiveID = nil
		return
	}
	s.ActiveID = cloneString(&s.Profiles[0].ID)
}

func (s state) active() (Profile, bool) {
	if s.ActiveID == nil {
		return Profile{}, false
	}
	for _, profile := range s.Profiles {
		if profile.ID == *s.ActiveID {
			return profile, true
		}
	}
	return Profile{}, false
}

func (s state) find(id string) (Profile, bool) {
	for _, profile := range s.Profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return Profile{}, false
}

func (s state) overview() Overview {
	result := Overview{Profiles: make([]Profile, len(s.Profiles)), ActiveID: cloneString(s.ActiveID)}
	for index, profile := range s.Profiles {
		masked := cloneProfile(profile)
		if masked.hasKeyMaterial() {
			masked.APIKey = MaskedAPIKey
		}
		result.Profiles[index] = masked
	}
	return result
}

func (s state) clone() state {
	profiles := make([]Profile, len(s.Profiles))
	for index, profile := range s.Profiles {
		profiles[index] = cloneProfile(profile)
	}
	return state{Version: StoreVersion, Profiles: profiles, ActiveID: cloneString(s.ActiveID)}
}
