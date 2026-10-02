package profiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const (
	SettingKey       = "ai.models"
	LegacySettingKey = "ai.provider"
	StoreVersion     = 1
)

var (
	ErrNoActiveProfile = errors.New("no active AI model profile")
	ErrProfileNotFound = errors.New("AI model profile not found")
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
	settings Settings
	mu       sync.RWMutex
	state    state
}

func NewManager(ctx context.Context, settings Settings) (*Manager, error) {
	if settings == nil {
		return nil, errors.New("AI profile settings store is nil")
	}
	loaded, needsSave, err := load(ctx, settings)
	if err != nil {
		return nil, err
	}
	if needsSave {
		if err := save(ctx, settings, loaded); err != nil {
			return nil, err
		}
	}
	return &Manager{settings: settings, state: loaded}, nil
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
	return profile.ProviderConfig(), true
}

func (m *Manager) ActiveClient(options ...provider.Option) (*provider.Client, error) {
	config, ok := m.ActiveConfig()
	if !ok {
		return nil, ErrNoActiveProfile
	}
	return provider.NewClient(config, options...)
}

func (m *Manager) Save(ctx context.Context, profile Profile) (Overview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.state.clone()
	profile = profile.Normalized()
	if profile.ID == "" {
		profile.ID = ids.New()
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
	if err := save(ctx, m.settings, next); err != nil {
		return m.state.overview(), err
	}
	m.state = next
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
	if err := save(ctx, m.settings, next); err != nil {
		return m.state.overview(), err
	}
	m.state = next
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
	if err := save(ctx, m.settings, next); err != nil {
		return m.state.overview(), err
	}
	m.state = next
	return m.state.overview(), nil
}

func (m *Manager) Reload(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	loaded, needsSave, err := load(ctx, m.settings)
	if err != nil {
		return err
	}
	if needsSave {
		if err := save(ctx, m.settings, loaded); err != nil {
			return err
		}
	}
	m.state = loaded
	return nil
}

func load(ctx context.Context, settings Settings) (state, bool, error) {
	raw, found, err := settings.SettingGet(ctx, SettingKey)
	if err != nil {
		return state{}, false, err
	}
	if found {
		var persisted state
		if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
			return state{Version: StoreVersion, Profiles: []Profile{}}, false, nil
		}
		if persisted.Version < 0 || persisted.Version > StoreVersion {
			return state{}, false, fmt.Errorf("unsupported AI profile store version %d", persisted.Version)
		}
		before, _ := json.Marshal(persisted)
		persisted = persisted.normalized()
		after, _ := json.Marshal(persisted)
		return persisted, !bytes.Equal(before, after), nil
	}

	legacyRaw, found, err := settings.SettingGet(ctx, LegacySettingKey)
	if err != nil || !found {
		return emptyState(), false, err
	}
	var legacy provider.Config
	if err := json.Unmarshal([]byte(legacyRaw), &legacy); err != nil {
		return emptyState(), false, nil
	}
	if legacy.BaseURL == "" && legacy.Model == "" && legacy.APIKey == "" {
		return emptyState(), false, nil
	}
	profile := Profile{
		ID: ids.New(), BaseURL: legacy.BaseURL, APIKey: legacy.APIKey, Model: legacy.Model,
		FallbackModel: legacy.FallbackModel, Temperature: legacy.Temperature, ContextWindow: legacy.ContextWindow,
		Proxy: legacy.Proxy, Stream: legacy.Stream,
	}.Normalized()
	loaded := state{Version: StoreVersion, Profiles: []Profile{profile}, ActiveID: cloneString(&profile.ID)}
	return loaded, true, nil
}

func save(ctx context.Context, settings Settings, value state) error {
	value = value.normalized()
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode AI profiles: %w", err)
	}
	return settings.SettingSet(ctx, SettingKey, string(encoded))
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

func (s state) overview() Overview {
	result := Overview{Profiles: make([]Profile, len(s.Profiles)), ActiveID: cloneString(s.ActiveID)}
	for index, profile := range s.Profiles {
		result.Profiles[index] = cloneProfile(profile)
	}
	return result
}

func (s state) clone() state {
	return state{Version: StoreVersion, Profiles: s.overview().Profiles, ActiveID: cloneString(s.ActiveID)}
}
