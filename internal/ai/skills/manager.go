package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ids"
)

const (
	SettingKey   = "ai.skills"
	StoreVersion = 1
)

// SettingKeyForUser mirrors the per-owner settings key layout used by the
// other AI services without requiring store-package changes.
func SettingKeyForUser(userID string) string {
	if userID == "" {
		return SettingKey
	}
	return "user.ai.skills." + userID
}

// Settings is the persistence contract required by Manager.
type Settings interface {
	SettingGet(ctx context.Context, key string) (value string, found bool, err error)
	SettingSet(ctx context.Context, key, value string) error
}

type Manager struct {
	settings Settings
	index    *Index
	shared   bool
	mu       sync.RWMutex
	states   map[string]*ownerState
}

type ownerState struct {
	overrides map[string]Override
}

type options struct {
	shared bool
	index  *Index
}

// Option customizes a Manager.
type Option func(*options)

// WithShared selects the legacy shared state for desktop and explicit open deployments.
func WithShared(shared bool) Option {
	return func(options *options) { options.shared = shared }
}

// WithIndex replaces the built-in embedded index, typically with a synthetic fixture.
func WithIndex(index *Index) Option {
	return func(options *options) { options.index = index }
}

// NewManager builds a Manager over the embedded skill index and the
// per-owner override store.
func NewManager(ctx context.Context, settings Settings, opts ...Option) (*Manager, error) {
	if settings == nil {
		return nil, errors.New("AI skill settings store is nil")
	}
	configuration := options{shared: true}
	for _, apply := range opts {
		apply(&configuration)
	}
	if configuration.index == nil {
		index, err := defaultIndex()
		if err != nil {
			return nil, fmt.Errorf("build embedded skill index: %w", err)
		}
		configuration.index = index
	}
	manager := &Manager{settings: settings, index: configuration.index, shared: configuration.shared, states: map[string]*ownerState{}}
	if _, err := owner.ID(ctx, manager.shared); err == nil {
		if err := manager.Reload(ctx); err != nil {
			return nil, err
		}
	}
	return manager, nil
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
	result, err := load(ctx, m.settings, ownerID, m.index)
	if err != nil {
		return nil, err
	}
	entry := &ownerState{overrides: result.overrides}
	if result.needsSave {
		saved, err := save(ctx, m.settings, ownerID, result.overrides)
		if err != nil {
			return nil, err
		}
		entry.overrides = saved
	}
	m.states[ownerID] = entry
	return entry, nil
}

// List returns the effective per-owner view of every embedded and custom
// skill, sorted by name. Disabled skills stay visible so they can be
// re-enabled from Settings.
func (m *Manager) List(ctx context.Context) ([]Entry, error) {
	_, entry, err := m.stateFor(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := map[string]struct{}{}
	for _, name := range m.index.Names() {
		names[name] = struct{}{}
	}
	for name := range entry.overrides {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	result := make([]Entry, 0, len(ordered))
	for _, name := range ordered {
		override, hasOverride := entry.overrides[name]
		result = append(result, m.entryFor(name, override, hasOverride))
	}
	return result, nil
}

func (m *Manager) entryFor(name string, override Override, hasOverride bool) Entry {
	base, hasBase := m.index.Lookup(name)
	skill := effective(base, hasBase, override)
	entry := Entry{
		Name:        name,
		Description: skill.Description,
		Source:      SourceEmbedded,
		Digest:      skill.Digest(),
	}
	if !hasBase {
		entry.Source = SourceCustom
	}
	if hasOverride {
		entry.UpdatedAt = override.UpdatedAt
		entry.Disabled = override.Disabled
		entry.Overridden = hasBase && (override.Description != nil || override.Body != nil)
		entry.Override = &Override{
			Name:        override.Name,
			Description: cloneString(override.Description),
			Body:        cloneString(override.Body),
			Disabled:    override.Disabled,
			UpdatedAt:   override.UpdatedAt,
		}
	}
	return entry
}

// SaveOverride creates or replaces the per-owner override for a skill. For
// an embedded name the override may replace the description, the body, or
// disable the skill; for an unknown name it creates a custom skill, which
// requires both description and body.
func (m *Manager) SaveOverride(ctx context.Context, input OverrideInput) (Entry, error) {
	name := strings.TrimSpace(input.Name)
	if err := validateName(name); err != nil {
		return Entry{}, err
	}
	_, hasBase := m.index.Lookup(name)
	if err := validateOverrideInput(name, hasBase, input); err != nil {
		return Entry{}, err
	}
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return Entry{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.stateForLocked(ctx, ownerID)
	if err != nil {
		return Entry{}, err
	}
	next := cloneOverrides(entry.overrides)
	override := Override{
		Name:        name,
		Description: cloneString(input.Description),
		Body:        cloneString(input.Body),
		Disabled:    input.Disabled,
		UpdatedAt:   ids.NowMS(),
	}
	// Keep updated_at strictly increasing so a newer edit always wins a sync.
	if previous, ok := entry.overrides[name]; ok {
		override.UpdatedAt = max(override.UpdatedAt, previous.UpdatedAt+1)
	}
	if override.Description != nil {
		trimmed := strings.TrimSpace(*override.Description)
		override.Description = &trimmed
	}
	next[name] = override
	saved, err := save(ctx, m.settings, ownerID, next)
	if err != nil {
		return Entry{}, err
	}
	entry.overrides = saved
	return m.entryFor(name, override, true), nil
}

// DeleteOverride removes the per-owner override or custom skill, reverting
// an embedded skill to its built-in content.
func (m *Manager) DeleteOverride(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return err
	}
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.stateForLocked(ctx, ownerID)
	if err != nil {
		return err
	}
	if _, ok := entry.overrides[name]; !ok {
		return fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}
	next := cloneOverrides(entry.overrides)
	delete(next, name)
	saved, err := m.saveDeletion(ctx, ownerID, next, name)
	if err != nil {
		return err
	}
	entry.overrides = saved
	return nil
}

// Dispatch resolves the effective skill material for the load_skill path and
// pins the digest of exactly what would be sent to the model. Callers
// record the digest as the dispatch evidence; overrides only ever change
// the returned instructions. Disabled skills are rejected here — Settings
// reads go through Material instead.
func (m *Manager) Dispatch(ctx context.Context, name string) (Dispatch, error) {
	dispatch, disabled, err := m.material(ctx, name)
	if err != nil {
		return Dispatch{}, err
	}
	if disabled {
		return Dispatch{}, fmt.Errorf("%w: %s", ErrSkillDisabled, name)
	}
	return dispatch, nil
}

// Material returns the effective skill material for Settings reads. Unlike
// Dispatch it also reads disabled skills, keeping Settings reads separate
// from model dispatch evidence.
func (m *Manager) Material(ctx context.Context, name string) (Dispatch, error) {
	dispatch, _, err := m.material(ctx, name)
	return dispatch, err
}

func (m *Manager) material(ctx context.Context, name string) (Dispatch, bool, error) {
	_, entry, err := m.stateFor(ctx)
	if err != nil {
		return Dispatch{}, false, err
	}
	m.mu.RLock()
	override, hasOverride := entry.overrides[name]
	m.mu.RUnlock()
	base, hasBase := m.index.Lookup(name)
	if !hasBase && !hasOverride {
		return Dispatch{}, false, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}
	skill := effective(base, hasBase, override)
	source := SourceEmbedded
	if !hasBase {
		source = SourceCustom
	}
	return Dispatch{
		Name:        skill.Name,
		Description: skill.Description,
		Body:        skill.Body,
		Digest:      skill.Digest(),
		Source:      source,
	}, hasOverride && override.Disabled, nil
}

// Reload re-reads the caller's override state from the settings store.
func (m *Manager) Reload(ctx context.Context) error {
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result, err := load(ctx, m.settings, ownerID, m.index)
	if err != nil {
		return err
	}
	entry := m.states[ownerID]
	if entry == nil {
		entry = &ownerState{overrides: map[string]Override{}}
		m.states[ownerID] = entry
	}
	entry.overrides = result.overrides
	if result.needsSave {
		saved, err := save(ctx, m.settings, ownerID, result.overrides)
		if err != nil {
			return err
		}
		entry.overrides = saved
	}
	return nil
}

type loadResult struct {
	overrides map[string]Override
	needsSave bool
}

func load(ctx context.Context, settings Settings, ownerID string, index *Index) (loadResult, error) {
	raw, found, err := settings.SettingGet(ctx, SettingKeyForUser(ownerID))
	if err != nil {
		return loadResult{}, err
	}
	if !found {
		return loadResult{overrides: map[string]Override{}}, nil
	}
	var persisted persistedState
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		slog.Warn("AI 技能数据损坏，已隔离并替换为空状态", "error", err)
		return loadResult{overrides: map[string]Override{}, needsSave: true}, nil
	}
	if persisted.Version <= 0 || persisted.Version > StoreVersion {
		return loadResult{}, fmt.Errorf("unsupported AI skill store version %d", persisted.Version)
	}
	before, _ := json.Marshal(persisted)
	normalized := normalizeOverrides(persisted.Overrides, index)
	after, _ := json.Marshal(persistedState{Version: StoreVersion, Overrides: normalized})
	return loadResult{overrides: overrideMap(normalized), needsSave: !bytes.Equal(before, after)}, nil
}

func save(ctx context.Context, settings Settings, ownerID string, overrides map[string]Override) (map[string]Override, error) {
	encoded, ordered, err := encodeOverrides(overrides)
	if err != nil {
		return nil, err
	}
	if err := settings.SettingSet(ctx, SettingKeyForUser(ownerID), encoded); err != nil {
		return nil, err
	}
	return overrideMap(ordered), nil
}

func encodeOverrides(overrides map[string]Override) (string, []Override, error) {
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	ordered := make([]Override, 0, len(names))
	for _, name := range names {
		ordered = append(ordered, overrides[name])
	}
	encoded, err := json.Marshal(persistedState{Version: StoreVersion, Overrides: ordered})
	if err != nil {
		return "", nil, fmt.Errorf("encode AI skills: %w", err)
	}
	return string(encoded), ordered, nil
}

type persistedState struct {
	Version   int        `json:"version"`
	Overrides []Override `json:"overrides"`
}

// normalizeOverrides drops persisted entries that no longer pass name
// validation and keeps the first entry per name so a poisoned or duplicated
// store cannot make resolution nondeterministic. Every retained entry must
// also pass the same semantic rules as SaveOverride against the current
// embedded index: blank or oversized fields, empty embedded overrides,
// custom skills missing description or body, and overrides whose embedded
// base has disappeared are all quarantined instead of being dispatched.
func normalizeOverrides(overrides []Override, index *Index) []Override {
	seen := make(map[string]struct{}, len(overrides))
	result := make([]Override, 0, len(overrides))
	for _, candidate := range overrides {
		override := candidate
		override.Name = strings.TrimSpace(override.Name)
		if validateName(override.Name) != nil {
			continue
		}
		if _, duplicate := seen[override.Name]; duplicate {
			continue
		}
		_, hasBase := index.Lookup(override.Name)
		if err := validateOverrideInput(override.Name, hasBase, OverrideInput{
			Name:        override.Name,
			Description: override.Description,
			Body:        override.Body,
			Disabled:    override.Disabled,
		}); err != nil {
			continue
		}
		// Load applies the same canonicalization as SaveOverride: the
		// trimmed description is what gets validated, persisted, and
		// dispatched, so padding cannot bypass the effective byte cap.
		if override.Description != nil {
			trimmed := strings.TrimSpace(*override.Description)
			override.Description = &trimmed
		}
		seen[override.Name] = struct{}{}
		result = append(result, override)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func overrideMap(overrides []Override) map[string]Override {
	result := make(map[string]Override, len(overrides))
	for _, override := range overrides {
		result[override.Name] = override
	}
	return result
}

func cloneOverrides(overrides map[string]Override) map[string]Override {
	result := make(map[string]Override, len(overrides))
	for name, override := range overrides {
		override.Description = cloneString(override.Description)
		override.Body = cloneString(override.Body)
		result[name] = override
	}
	return result
}
