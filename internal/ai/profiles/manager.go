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

	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

const (
	SettingKey          = store.AIProfilesSettingKey
	LegacySettingKey    = store.AILegacySettingKey
	ScrubPendingSetting = store.AIScrubPendingSettingKey
	StoreVersion        = 1
)

var (
	ErrNoActiveProfile       = errors.New("no active AI model profile")
	ErrProfileNotFound       = errors.New("AI model profile not found")
	ErrProfileKeyUnavailable = errors.New("AI model profile has no usable API key")
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

type Manager struct {
	settings  Settings
	protector store.SecretProtector
	mu        sync.RWMutex
	state     state
	circuits  map[string]circuitEntry
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

type settingDeleter interface {
	SettingDelete(ctx context.Context, key string) error
}

type migrationTx interface {
	SettingSetManyDelete(ctx context.Context, values map[string]string, deleteKeys ...string) error
}

type aiScrubFinalizer interface {
	AISettingsFinalizeScrub(ctx context.Context, expectedGeneration int64) (bool, error)
}

type spaceScrubber interface {
	ScrubFreeSpace(ctx context.Context) error
}

type loadResult struct {
	state          state
	needsSave      bool
	fromLegacy     bool
	legacyLeftover bool
	migratable     bool
}

func NewManager(ctx context.Context, settings Settings) (*Manager, error) {
	if settings == nil {
		return nil, errors.New("AI profile settings store is nil")
	}
	var protector store.SecretProtector
	if source, ok := settings.(protectorSource); ok {
		protector = source.SecretProtector()
	}
	result, err := load(ctx, settings, protector)
	if err != nil {
		return nil, err
	}
	manager := &Manager{settings: settings, protector: protector, state: result.state, circuits: map[string]circuitEntry{}}
	if err := manager.applyLoadResult(ctx, result); err != nil {
		return nil, err
	}
	if listener, ok := protector.(unlockListenerSource); ok {
		listener.AddUnlockListener(manager.reloadOnUnlock)
	}
	return manager, nil
}

func (m *Manager) applyLoadResult(ctx context.Context, result loadResult) error {
	deleteLegacy := result.fromLegacy || result.legacyLeftover
	savedOK := false
	if result.needsSave {
		saved, err := save(ctx, m.settings, m.protector, result.state, deleteLegacy)
		if err != nil {
			if !isVaultUnavailable(err) && !store.IsBusy(err) {
				return err
			}
		} else {
			m.state = saved
			savedOK = true
		}
	} else if deleteLegacy {
		if deleter, ok := m.settings.(settingDeleter); ok {
			if err := deleter.SettingDelete(ctx, LegacySettingKey); err != nil && !store.IsBusy(err) {
				return err
			}
		}
	}
	if !savedOK {
		m.state = result.state
	}
	return m.finalizeScrub(ctx)
}

func (m *Manager) reloadOnUnlock() {
	if err := m.Reload(context.Background()); err != nil {
		slog.Warn("凭据库解锁后刷新 AI 模型档案失败", "error", err)
	}
}

func (m *Manager) Overview() Overview {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.overview()
}

func (m *Manager) ActiveProfile() (Profile, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	profile, ok := m.state.active()
	if !ok {
		return Profile{}, false
	}
	return cloneProfile(profile), true
}

func (m *Manager) ActiveConfig() (provider.Config, bool) {
	profile, ok := m.ActiveProfile()
	if !ok {
		return provider.Config{}, false
	}
	config := profile.ProviderConfig()
	key, err := m.resolveAPIKey(profile)
	if err != nil {
		config.APIKey = ""
	} else {
		config.APIKey = key
	}
	return config, true
}

func (m *Manager) ActiveClient(options ...provider.Option) (*provider.Client, error) {
	m.mu.RLock()
	profile, ok := m.state.active()
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
	clientOptions := append(profile.ClientOptions(), m.circuitOption(profile))
	return provider.NewClient(config, append(clientOptions, options...)...)
}

func (m *Manager) circuitOption(profile Profile) provider.Option {
	threshold, cooldown := profile.CircuitConfig()
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry, ok := m.circuits[profile.ID]; ok && entry.threshold == threshold && entry.cooldown == cooldown {
		return provider.WithCircuitBreaker(entry.breaker)
	}
	breaker := provider.NewCircuitBreaker(threshold, cooldown)
	m.circuits[profile.ID] = circuitEntry{breaker: breaker, threshold: threshold, cooldown: cooldown}
	return provider.WithCircuitBreaker(breaker)
}

type CircuitStatus struct {
	ConsecutiveFailures int        `json:"consecutiveFailures"`
	OpenUntil           *time.Time `json:"openUntil,omitempty"`
}

func (m *Manager) CircuitStatus(id string) (CircuitStatus, bool) {
	m.mu.RLock()
	entry, ok := m.circuits[id]
	m.mu.RUnlock()
	if !ok {
		return CircuitStatus{}, false
	}
	snapshot := entry.breaker.Snapshot()
	status := CircuitStatus{ConsecutiveFailures: snapshot.ConsecutiveFailures}
	if !snapshot.OpenUntil.IsZero() {
		openUntil := snapshot.OpenUntil
		status.OpenUntil = &openUntil
	}
	return status, true
}

func (m *Manager) resolveAPIKey(profile Profile) (string, error) {
	switch {
	case profile.APIKey == "":
		return "", nil
	case profile.APIKey == MaskedAPIKey:
		return "", fmt.Errorf("%w: %s", ErrProfileKeyUnavailable, profile.Name)
	case m.protector == nil:
		return profile.APIKey, nil
	case strings.HasPrefix(profile.APIKey, store.SecretEnvelopePrefix):
		plaintext, err := m.protector.DecryptSecret(context.Background(), profile.APIKey)
		if err != nil {
			return "", err
		}
		if plaintext == MaskedAPIKey {
			return "", fmt.Errorf("%w: %s", ErrProfileKeyUnavailable, profile.Name)
		}
		return plaintext, nil
	default:
		if _, err := m.protector.EncryptSecret(context.Background(), profile.APIKey); err != nil {
			return "", ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
		}
		return profile.APIKey, nil
	}
}

func (m *Manager) Profile(id string) (Profile, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	profile, ok := m.state.find(id)
	if !ok {
		return Profile{}, false
	}
	return cloneProfile(profile), true
}

func (m *Manager) ClientFor(id string, options ...provider.Option) (*provider.Client, error) {
	if id == "" {
		return m.ActiveClient(options...)
	}
	m.mu.RLock()
	profile, ok := m.state.find(id)
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
	clientOptions := append(profile.ClientOptions(), m.circuitOption(profile))
	return provider.NewClient(config, append(clientOptions, options...)...)
}

func (m *Manager) Save(ctx context.Context, profile Profile) (Overview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.state.clone()
	profile = profile.Normalized()
	if profile.ID == "" {
		profile.ID = ids.New()
	}
	if profile.APIKey == MaskedAPIKey || (m.protector != nil && strings.HasPrefix(profile.APIKey, store.SecretEnvelopePrefix)) {
		if existing, ok := m.state.find(profile.ID); ok {
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
	saved, err := save(ctx, m.settings, m.protector, next, m.deleteLegacyWithSave(ctx))
	if err != nil {
		return m.state.overview(), err
	}
	m.state = saved
	if err := m.finalizeScrub(ctx); err != nil {
		return m.state.overview(), err
	}
	return m.state.overview(), nil
}

func (m *Manager) Activate(ctx context.Context, id string) (Overview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	found := false
	for _, profile := range m.state.Profiles {
		if profile.ID == id {
			found = true
			break
		}
	}
	if !found {
		return m.state.overview(), fmt.Errorf("%w: %s", ErrProfileNotFound, id)
	}
	next := m.state.clone()
	next.ActiveID = cloneString(&id)
	saved, err := save(ctx, m.settings, m.protector, next, m.deleteLegacyWithSave(ctx))
	if err != nil {
		return m.state.overview(), err
	}
	m.state = saved
	if err := m.finalizeScrub(ctx); err != nil {
		return m.state.overview(), err
	}
	return m.state.overview(), nil
}

func (m *Manager) Delete(ctx context.Context, id string) (Overview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.state.clone()
	profiles := make([]Profile, 0, len(next.Profiles))
	for _, profile := range next.Profiles {
		if profile.ID != id {
			profiles = append(profiles, profile)
		}
	}
	if len(profiles) == len(next.Profiles) {
		return m.state.overview(), nil
	}
	next.Profiles = profiles
	next.ensureActive()
	saved, err := save(ctx, m.settings, m.protector, next, m.deleteLegacyWithSave(ctx))
	if err != nil {
		return m.state.overview(), err
	}
	m.state = saved
	delete(m.circuits, id)
	if err := m.finalizeScrub(ctx); err != nil {
		return m.state.overview(), err
	}
	return m.state.overview(), nil
}

func (m *Manager) Reload(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	result, err := load(ctx, m.settings, m.protector)
	if err != nil {
		return err
	}
	return m.applyLoadResult(ctx, result)
}

func (m *Manager) deleteLegacyWithSave(ctx context.Context) bool {
	return m.protector != nil && aiLegacySensitiveRowPresent(ctx, m.settings)
}

func (m *Manager) finalizeScrub(ctx context.Context) error {
	pending, err := scrubPending(ctx, m.settings)
	if err != nil || !pending {
		return err
	}
	if aiLegacySensitiveRowPresent(ctx, m.settings) {
		return nil
	}
	generation, err := aiGeneration(ctx, m.settings)
	if err != nil {
		return err
	}
	if err := scrubSettings(ctx, m.settings); err != nil {
		if !errors.Is(err, store.ErrScrubBusy) && !store.IsBusy(err) {
			slog.Warn("AI 模型档案物理清理失败，将在后续启动、解锁或写入时重试", "error", err)
		}
		return nil
	}
	if finalizer, ok := m.settings.(aiScrubFinalizer); ok {
		_, err := finalizer.AISettingsFinalizeScrub(ctx, generation)
		return err
	}
	return clearScrubPending(ctx, m.settings)
}

func load(ctx context.Context, settings Settings, protector store.SecretProtector) (loadResult, error) {
	legacyRaw, legacyFound, err := settings.SettingGet(ctx, LegacySettingKey)
	if err != nil {
		return loadResult{}, err
	}
	var legacy provider.Config
	legacyValid := legacyFound && json.Unmarshal([]byte(legacyRaw), &legacy) == nil
	legacyUsable := legacyValid && (legacy.BaseURL != "" || legacy.Model != "" || legacy.APIKey != "")
	legacySensitive := legacyFound && store.AISettingValueSensitive(LegacySettingKey, legacyRaw)

	raw, found, err := settings.SettingGet(ctx, SettingKey)
	if err != nil {
		return loadResult{}, err
	}
	if found {
		var persisted state
		if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
			slog.Warn("AI 模型档案数据损坏，已隔离并替换为空状态", "error", err)
			return loadResult{
				state: state{Version: StoreVersion, Profiles: []Profile{}}, needsSave: true,
				legacyLeftover: legacySensitive,
			}, nil
		}
		if persisted.Version < 0 || persisted.Version > StoreVersion {
			return loadResult{}, fmt.Errorf("unsupported AI profile store version %d", persisted.Version)
		}
		before, _ := json.Marshal(persisted)
		persisted = persisted.normalized()
		after, _ := json.Marshal(persisted)
		migratable := profileKeysMigratable(ctx, protector, &persisted)
		return loadResult{
			state: persisted, needsSave: !bytes.Equal(before, after) || migratable,
			legacyLeftover: legacySensitive, migratable: migratable,
		}, nil
	}

	if legacyUsable {
		return legacyMigrationResult(legacy), nil
	}
	if legacySensitive {
		return loadResult{state: emptyState(), legacyLeftover: true}, nil
	}
	return loadResult{state: emptyState()}, nil
}

func legacyMigrationResult(legacy provider.Config) loadResult {
	profile := Profile{
		ID: ids.New(), BaseURL: legacy.BaseURL, APIKey: legacy.APIKey, Model: legacy.Model,
		FallbackModel: legacy.FallbackModel, Temperature: legacy.Temperature, ContextWindow: legacy.ContextWindow,
		Proxy: legacy.Proxy, Stream: legacy.Stream,
	}.Normalized()
	loaded := state{Version: StoreVersion, Profiles: []Profile{profile}, ActiveID: cloneString(&profile.ID)}
	return loadResult{state: loaded, needsSave: true, fromLegacy: true}
}

func aiLegacySensitiveRowPresent(ctx context.Context, settings Settings) bool {
	raw, found, err := settings.SettingGet(ctx, LegacySettingKey)
	if err != nil || !found {
		return false
	}
	return store.AISettingValueSensitive(LegacySettingKey, raw)
}

func profileKeysMigratable(ctx context.Context, protector store.SecretProtector, persisted *state) bool {
	if protector == nil {
		return false
	}
	for _, profile := range persisted.Profiles {
		if profile.APIKey == "" || strings.HasPrefix(profile.APIKey, store.SecretEnvelopePrefix) {
			continue
		}
		if _, err := protector.EncryptSecret(ctx, profile.APIKey); err == nil {
			return true
		}
	}
	return false
}

func save(ctx context.Context, settings Settings, protector store.SecretProtector, value state, deleteLegacy bool) (state, error) {
	value = value.normalized()
	if protector != nil {
		for index := range value.Profiles {
			profile := &value.Profiles[index]
			if profile.APIKey == "" || strings.HasPrefix(profile.APIKey, store.SecretEnvelopePrefix) {
				continue
			}
			envelope, err := protector.EncryptSecret(ctx, profile.APIKey)
			if err != nil {
				return value, err
			}
			profile.APIKey = envelope
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return value, fmt.Errorf("encode AI profiles: %w", err)
	}
	if deleteLegacy {
		if tx, ok := settings.(migrationTx); ok {
			return value, tx.SettingSetManyDelete(ctx, map[string]string{SettingKey: string(encoded)}, LegacySettingKey)
		}
		if err := settings.SettingSet(ctx, SettingKey, string(encoded)); err != nil {
			return value, err
		}
		if deleter, ok := settings.(settingDeleter); ok {
			return value, deleter.SettingDelete(ctx, LegacySettingKey)
		}
		return value, nil
	}
	if err := settings.SettingSet(ctx, SettingKey, string(encoded)); err != nil {
		return value, err
	}
	return value, nil
}

func scrubPending(ctx context.Context, settings Settings) (bool, error) {
	raw, found, err := settings.SettingGet(ctx, ScrubPendingSetting)
	if err != nil || !found {
		return false, err
	}
	return raw != "", nil
}

func clearScrubPending(ctx context.Context, settings Settings) error {
	if deleter, ok := settings.(settingDeleter); ok {
		return deleter.SettingDelete(ctx, ScrubPendingSetting)
	}
	return settings.SettingSet(ctx, ScrubPendingSetting, "")
}

func scrubSettings(ctx context.Context, settings Settings) error {
	if scrubber, ok := settings.(spaceScrubber); ok {
		return scrubber.ScrubFreeSpace(ctx)
	}
	return nil
}

func aiGeneration(ctx context.Context, settings Settings) (int64, error) {
	if source, ok := settings.(interface {
		AISettingsGeneration(ctx context.Context) (int64, error)
	}); ok {
		return source.AISettingsGeneration(ctx)
	}
	raw, found, err := settings.SettingGet(ctx, store.AIGenerationSettingKey)
	if err != nil || !found {
		return 0, err
	}
	return aiGenerationFromRaw(raw), nil
}

func aiGenerationFromRaw(raw string) int64 {
	var generation int64
	_, _ = fmt.Sscanf(raw, "%d", &generation)
	return generation
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
