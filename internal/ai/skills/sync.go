package skills

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// SyncTombstoneKind marks a removed override or custom skill in the sync tombstone table.
const SyncTombstoneKind = "skill"

// SyncObjectPrefix prefixes a skill name to form its sync object ID.
const SyncObjectPrefix = "skill/"

// ErrSyncConflict reports that the owner's override changed after the
// caller read it; the synced write was not applied.
var ErrSyncConflict = errors.New("AI 技能在同步期间被修改")

// tombstoneSettings is implemented by the store: it saves the overrides and
// records the deletion of one in a single transaction, so other devices of the
// owner remove it too.
type tombstoneSettings interface {
	SettingSetWithTombstone(ctx context.Context, key, value, id, kind string, deletedAt int64) error
}

// SyncObjectID is the sync object ID of the named override.
func SyncObjectID(name string) string { return SyncObjectPrefix + name }

// Overrides returns the caller's persisted overrides and custom skills
// sorted by name. Built-in skills without an override are not included.
func (m *Manager) Overrides(ctx context.Context) ([]Override, error) {
	_, entry, err := m.stateFor(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Override, 0, len(entry.overrides))
	for _, override := range cloneOverrides(entry.overrides) {
		result = append(result, override)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// ValidateSynced checks an override received through sync against the same
// rules as SaveOverride and this build's built-in skills.
func (m *Manager) ValidateSynced(override Override) error {
	if err := validateName(override.Name); err != nil {
		return err
	}
	if override.Description != nil && strings.TrimSpace(*override.Description) != *override.Description {
		return fmt.Errorf("%w: untrimmed description for %q", ErrInvalidSkill, override.Name)
	}
	_, hasBase := m.index.Lookup(override.Name)
	return validateOverrideInput(override.Name, hasBase, OverrideInput{
		Name: override.Name, Description: override.Description, Body: override.Body, Disabled: override.Disabled,
	})
}

func sameOverride(a, b Override) bool {
	same := func(x, y *string) bool { return x == nil && y == nil || x != nil && y != nil && *x == *y }
	return a.Name == b.Name && a.Disabled == b.Disabled && a.UpdatedAt == b.UpdatedAt &&
		same(a.Description, b.Description) && same(a.Body, b.Body)
}

// ApplySynced stores an override received through sync with its peer
// revision when the caller's override still equals expected (nil: none).
func (m *Manager) ApplySynced(ctx context.Context, override Override, expected *Override) error {
	if err := m.ValidateSynced(override); err != nil {
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
	current, ok := entry.overrides[override.Name]
	if ok != (expected != nil) || ok && !sameOverride(current, *expected) {
		return ErrSyncConflict
	}
	next := cloneOverrides(entry.overrides)
	next[override.Name] = Override{
		Name: override.Name, Description: cloneString(override.Description), Body: cloneString(override.Body),
		Disabled: override.Disabled, UpdatedAt: override.UpdatedAt,
	}
	saved, err := save(ctx, m.settings, ownerID, next)
	if err != nil {
		return err
	}
	entry.overrides = saved
	return nil
}

// RemoveSynced applies a synced deletion: the caller's override is removed
// unless it changed after deletedAt. It reports whether the deletion took
// effect (an absent override counts as removed).
func (m *Manager) RemoveSynced(ctx context.Context, name string, deletedAt int64) (bool, error) {
	if err := validateName(name); err != nil {
		return false, err
	}
	ownerID, err := owner.ID(ctx, m.shared)
	if err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.stateForLocked(ctx, ownerID)
	if err != nil {
		return false, err
	}
	current, ok := entry.overrides[name]
	if !ok {
		return true, nil
	}
	if current.UpdatedAt > deletedAt {
		return false, nil
	}
	next := cloneOverrides(entry.overrides)
	delete(next, name)
	saved, err := save(ctx, m.settings, ownerID, next)
	if err != nil {
		return false, err
	}
	entry.overrides = saved
	return true, nil
}

// saveDeletion saves overrides without the removed name and, when the
// settings store supports it, records the removal's sync tombstone in the
// same transaction.
func (m *Manager) saveDeletion(ctx context.Context, ownerID string, overrides map[string]Override, removed string) (map[string]Override, error) {
	writer, ok := m.settings.(tombstoneSettings)
	if !ok {
		return save(ctx, m.settings, ownerID, overrides)
	}
	encoded, ordered, err := encodeOverrides(overrides)
	if err != nil {
		return nil, err
	}
	if err := writer.SettingSetWithTombstone(ctx, SettingKeyForUser(ownerID), encoded, SyncObjectID(removed), SyncTombstoneKind, ids.NowMS()); err != nil {
		return nil, err
	}
	return overrideMap(ordered), nil
}
