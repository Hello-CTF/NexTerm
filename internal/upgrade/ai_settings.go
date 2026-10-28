package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// openAISettingsCopiedKeyPrefix marks, per target owner, the fingerprint of the
// Open-mode AI settings already copied. The copy reruns only when those global
// settings change (for example after another Open-mode session), so entries
// the owner deleted afterwards are not restored on every account-mode start.
const openAISettingsCopiedKeyPrefix = "upgrade.open_ai_settings.user."

// openAISettingKeys are the global (Open-mode) AI settings copied to the owner.
var openAISettingKeys = []string{
	store.AIProfilesSettingKey,
	guard.PermissionSettingKey,
	guard.DeviceGrantsSettingKey,
	guard.GrantRulesSettingKey,
	guard.DevicePermissionSettingKey,
}

type AISettingsItemReport struct {
	Migrated   int64
	Existing   int64
	Conflicts  int64
	Tombstoned int64
}

// AISettingsReport counts global Open-mode AI settings copied to the owner.
// The global rows stay in place for Open mode.
type AISettingsReport struct {
	Profiles     AISettingsItemReport
	Permissions  AISettingsItemReport
	DeviceGrants AISettingsItemReport
	GrantRules   AISettingsItemReport
	DeviceModes  AISettingsItemReport
	Tombstones   AISettingsItemReport
}

func (r AISettingsReport) Total() AISettingsItemReport {
	var total AISettingsItemReport
	for _, item := range []AISettingsItemReport{r.Profiles, r.Permissions, r.DeviceGrants, r.GrantRules, r.DeviceModes, r.Tombstones} {
		total.Migrated += item.Migrated
		total.Existing += item.Existing
		total.Conflicts += item.Conflicts
		total.Tombstoned += item.Tombstoned
	}
	return total
}

func (r AISettingsReport) Empty() bool {
	return r.Total() == (AISettingsItemReport{})
}

func settingKeysIn(keys []string) clause.Expression {
	values := make([]any, len(keys))
	for index, key := range keys {
		values[index] = key
	}
	return clause.IN{Column: clause.Column{Name: "key"}, Values: values}
}

func userSettingKey(globalKey, ownerID string) string {
	if globalKey == store.AIProfilesSettingKey {
		return store.AIProfilesSettingKeyForUser(ownerID)
	}
	return globalKey + ".user." + ownerID
}

// aiSettingsMigrationPending is true while Open-mode AI settings exist that
// the first active superadmin has not received in their current form, or
// while no active superadmin exists to receive them.
func aiSettingsMigrationPending(database *gorm.DB) (bool, error) {
	found, err := hasRows(database.Model(&store.Setting{}).Where(settingKeysIn(openAISettingKeys)))
	if err == nil && !found {
		found, err = hasOwnerlessRows(database.Model(&store.SyncTombstone{}).Where("kind IN ?", aiOwnedTombstoneKinds))
	}
	if err != nil || !found {
		return found, err
	}
	ownerID, err := firstActiveSuperadmin(database)
	if err != nil || ownerID == "" {
		return true, err
	}
	m, err := newAISettingsMigration(database, ownerID, &AISettingsReport{})
	if err != nil {
		return false, err
	}
	fingerprint, err := m.fingerprint()
	if err != nil {
		return false, err
	}
	marker, copied := m.settings[openAISettingsCopiedKeyPrefix+ownerID]
	return !copied || marker.Value != fingerprint, nil
}

func hasRows(query *gorm.DB) (bool, error) {
	var found int
	result := query.Select("1").Limit(1).Scan(&found)
	return found == 1, result.Error
}

func migrateAISettings(database *gorm.DB, ownerID string, report *AISettingsReport) error {
	m, err := newAISettingsMigration(database, ownerID, report)
	if err != nil {
		return err
	}
	if len(m.sourceKeys) == 0 && len(m.tombstones) == 0 {
		return nil
	}
	fingerprint, err := m.fingerprint()
	if err != nil {
		return err
	}
	markerKey := openAISettingsCopiedKeyPrefix + ownerID
	if marker, found := m.settings[markerKey]; found && marker.Value == fingerprint {
		return nil
	}
	for _, step := range []func() error{
		m.migrateProfiles,
		m.migratePermission,
		m.migrateDeviceGrants,
		m.migrateGrantRules,
		m.migrateDeviceModes,
		m.migrateTombstones,
	} {
		if err := step(); err != nil {
			return err
		}
	}
	return m.putSetting(markerKey, fingerprint, 0)
}

type aiProfileState struct {
	Version  int                `json:"version"`
	Profiles []profiles.Profile `json:"profiles"`
	ActiveID *string            `json:"activeId"`
}

type aiSettingsMigration struct {
	database   *gorm.DB
	ownerID    string
	report     *AISettingsReport
	settings   map[string]store.Setting
	sourceKeys []string
	global     map[string]store.SyncTombstone
	target     map[string]store.SyncTombstone
	tombstones []store.SyncTombstone

	profiles aiProfileState
	grants   map[string]guard.DeviceGrant
	rules    []guard.GrantRule
	modes    map[string]guard.Mode
}

func newAISettingsMigration(database *gorm.DB, ownerID string, report *AISettingsReport) (*aiSettingsMigration, error) {
	keys := append([]string(nil), openAISettingKeys...)
	for _, key := range openAISettingKeys {
		keys = append(keys, userSettingKey(key, ownerID))
	}
	keys = append(keys, openAISettingsCopiedKeyPrefix+ownerID)
	var settingRows []store.Setting
	if err := database.Where(settingKeysIn(keys)).Find(&settingRows).Error; err != nil {
		return nil, err
	}
	m := &aiSettingsMigration{
		database: database, ownerID: ownerID, report: report,
		settings: make(map[string]store.Setting, len(settingRows)),
		global:   make(map[string]store.SyncTombstone), target: make(map[string]store.SyncTombstone),
	}
	for _, row := range settingRows {
		m.settings[row.Key] = row
	}
	for _, key := range openAISettingKeys {
		if _, found := m.settings[key]; found {
			m.sourceKeys = append(m.sourceKeys, key)
		}
	}

	var tombstoneRows []store.SyncTombstone
	if err := database.
		Where("owner_id IN ? AND kind IN ?", []string{"", ownerID}, aiOwnedTombstoneKinds).
		Order("owner_id").Order("kind").Order("id").
		Find(&tombstoneRows).Error; err != nil {
		return nil, err
	}
	for _, row := range tombstoneRows {
		if row.OwnerID == "" {
			m.global[row.ID] = row
			m.tombstones = append(m.tombstones, row)
		} else {
			m.target[row.ID] = row
		}
	}
	return m, nil
}

// fingerprint identifies the current global AI settings and tombstones.
func (m *aiSettingsMigration) fingerprint() (string, error) {
	type entry struct {
		Key, Value, Kind string
		DeletedAt        int64
	}
	entries := make([]entry, 0, len(m.sourceKeys)+len(m.tombstones))
	for _, key := range m.sourceKeys {
		entries = append(entries, entry{Key: key, Value: m.settings[key].Value})
	}
	for _, row := range m.tombstones {
		entries = append(entries, entry{Key: row.ID, Kind: row.Kind, DeletedAt: row.DeletedAt})
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (m *aiSettingsMigration) putSetting(key, value string, updatedAt int64) error {
	row := store.Setting{Key: key, Value: value, UpdatedAt: updatedAt}
	if err := m.database.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&row).Error; err != nil {
		return err
	}
	m.settings[key] = row
	return nil
}

func (m *aiSettingsMigration) tombstoned(id string) bool {
	_, global := m.global[id]
	_, target := m.target[id]
	return global || target
}

func (m *aiSettingsMigration) source(key string) (store.Setting, bool) {
	row, found := m.settings[key]
	return row, found
}

func (m *aiSettingsMigration) targetOf(key string) (string, store.Setting, bool) {
	targetKey := userSettingKey(key, m.ownerID)
	row, found := m.settings[targetKey]
	return targetKey, row, found
}

func (m *aiSettingsMigration) writeMerged(targetKey string, value any, sourceRow, targetRow store.Setting, targetFound bool) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	updatedAt := sourceRow.UpdatedAt
	if targetFound {
		updatedAt = max(updatedAt, targetRow.UpdatedAt)
	}
	return m.putSetting(targetKey, string(encoded), updatedAt)
}

func decodeAIProfiles(row store.Setting, found bool) (aiProfileState, error) {
	if !found {
		return aiProfileState{Version: profiles.StoreVersion, Profiles: []profiles.Profile{}}, nil
	}
	var state aiProfileState
	if err := json.Unmarshal([]byte(row.Value), &state); err != nil {
		return aiProfileState{}, fmt.Errorf("decode AI profiles: %w", err)
	}
	if state.Version <= 0 || state.Version > profiles.StoreVersion {
		return aiProfileState{}, fmt.Errorf("unsupported AI profile store version %d", state.Version)
	}
	if state.Profiles == nil {
		state.Profiles = []profiles.Profile{}
	}
	return state, nil
}

func findAIProfile(state aiProfileState, id string) (profiles.Profile, bool) {
	for _, profile := range state.Profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return profiles.Profile{}, false
}

func aiProfileNameConflict(state aiProfileState, candidate profiles.Profile) bool {
	name := strings.TrimSpace(candidate.Name)
	if name == "" {
		return false
	}
	for _, existing := range state.Profiles {
		if existing.ID != candidate.ID && strings.TrimSpace(existing.Name) == name {
			return true
		}
	}
	return false
}

func (m *aiSettingsMigration) migrateProfiles() error {
	sourceRow, sourceFound := m.source(store.AIProfilesSettingKey)
	targetKey, targetRow, targetFound := m.targetOf(store.AIProfilesSettingKey)
	source, err := decodeAIProfiles(sourceRow, sourceFound)
	if err != nil {
		return err
	}
	target, err := decodeAIProfiles(targetRow, targetFound)
	if err != nil {
		return err
	}
	m.profiles = target
	if !sourceFound {
		return nil
	}
	if !targetFound {
		target.Version = source.Version
	}
	added := false
	for _, profile := range source.Profiles {
		if profile.ID == "" {
			return errors.New("open-mode AI profile has an empty ID")
		}
		if m.tombstoned(profile.ID) {
			m.report.Profiles.Tombstoned++
			continue
		}
		if existing, ok := findAIProfile(target, profile.ID); ok {
			if reflect.DeepEqual(existing, profile) {
				m.report.Profiles.Existing++
			} else {
				m.report.Profiles.Conflicts++
			}
			continue
		}
		if aiProfileNameConflict(target, profile) {
			m.report.Profiles.Conflicts++
			continue
		}
		target.Profiles = append(target.Profiles, profile)
		m.report.Profiles.Migrated++
		added = true
	}
	m.profiles = target
	if !added {
		return nil
	}
	if target.ActiveID == nil || !func() bool { _, ok := findAIProfile(target, *target.ActiveID); return ok }() {
		target.ActiveID = nil
		if source.ActiveID != nil && !m.tombstoned(*source.ActiveID) {
			sourceActive, sourceOK := findAIProfile(source, *source.ActiveID)
			mergedActive, mergedOK := findAIProfile(target, *source.ActiveID)
			if sourceOK && mergedOK && reflect.DeepEqual(sourceActive, mergedActive) {
				target.ActiveID = source.ActiveID
			}
		}
		if target.ActiveID == nil && len(target.Profiles) > 0 {
			activeID := target.Profiles[0].ID
			target.ActiveID = &activeID
		}
	}
	m.profiles = target
	return m.writeMerged(targetKey, target, sourceRow, targetRow, targetFound)
}

func (m *aiSettingsMigration) migratePermission() error {
	sourceRow, sourceFound := m.source(guard.PermissionSettingKey)
	if !sourceFound || sourceRow.Value == "" {
		return nil
	}
	if m.tombstoned("global") {
		m.report.Permissions.Tombstoned++
		return nil
	}
	targetKey, targetRow, targetFound := m.targetOf(guard.PermissionSettingKey)
	if targetFound {
		var sourceConfig, targetConfig guard.Config
		if err := json.Unmarshal([]byte(sourceRow.Value), &sourceConfig); err != nil {
			return fmt.Errorf("decode open-mode AI permission: %w", err)
		}
		if err := json.Unmarshal([]byte(targetRow.Value), &targetConfig); err != nil {
			return fmt.Errorf("decode target AI permission: %w", err)
		}
		if reflect.DeepEqual(sourceConfig, targetConfig) {
			m.report.Permissions.Existing++
		} else {
			m.report.Permissions.Conflicts++
		}
		return nil
	}
	m.report.Permissions.Migrated++
	return m.putSetting(targetKey, sourceRow.Value, sourceRow.UpdatedAt)
}

func decodeAIMap[V any](row store.Setting, found bool, what string) (map[string]V, error) {
	result := make(map[string]V)
	if !found || row.Value == "" {
		return result, nil
	}
	if err := json.Unmarshal([]byte(row.Value), &result); err != nil {
		return nil, fmt.Errorf("decode AI %s: %w", what, err)
	}
	if result == nil {
		result = make(map[string]V)
	}
	return result, nil
}

// mergeAIMap adds source entries missing from target; it reports whether any
// entry was added.
func mergeAIMap[V any](m *aiSettingsMigration, source, target map[string]V, tombstonePrefix string, item *AISettingsItemReport) bool {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	added := false
	for _, key := range keys {
		value := source[key]
		if m.tombstoned(tombstonePrefix + key) {
			item.Tombstoned++
			continue
		}
		if existing, ok := target[key]; ok {
			if reflect.DeepEqual(existing, value) {
				item.Existing++
			} else {
				item.Conflicts++
			}
			continue
		}
		target[key] = value
		item.Migrated++
		added = true
	}
	return added
}

func (m *aiSettingsMigration) migrateDeviceGrants() error {
	sourceRow, sourceFound := m.source(guard.DeviceGrantsSettingKey)
	targetKey, targetRow, targetFound := m.targetOf(guard.DeviceGrantsSettingKey)
	source, err := decodeAIMap[guard.DeviceGrant](sourceRow, sourceFound, "device grants")
	if err != nil {
		return err
	}
	target, err := decodeAIMap[guard.DeviceGrant](targetRow, targetFound, "device grants")
	if err != nil {
		return err
	}
	m.grants = target
	if !mergeAIMap(m, source, target, guard.AIGrantSyncDevicePrefix, &m.report.DeviceGrants) {
		return nil
	}
	return m.writeMerged(targetKey, target, sourceRow, targetRow, targetFound)
}

func (m *aiSettingsMigration) migrateDeviceModes() error {
	sourceRow, sourceFound := m.source(guard.DevicePermissionSettingKey)
	targetKey, targetRow, targetFound := m.targetOf(guard.DevicePermissionSettingKey)
	source, err := decodeAIMap[guard.Mode](sourceRow, sourceFound, "device permission modes")
	if err != nil {
		return err
	}
	target, err := decodeAIMap[guard.Mode](targetRow, targetFound, "device permission modes")
	if err != nil {
		return err
	}
	m.modes = target
	if !mergeAIMap(m, source, target, guard.AIGrantSyncModePrefix, &m.report.DeviceModes) {
		return nil
	}
	return m.writeMerged(targetKey, target, sourceRow, targetRow, targetFound)
}

func decodeAIGrantRules(row store.Setting, found bool) ([]guard.GrantRule, error) {
	if !found || row.Value == "" {
		return []guard.GrantRule{}, nil
	}
	var rules []guard.GrantRule
	if err := json.Unmarshal([]byte(row.Value), &rules); err != nil {
		return nil, fmt.Errorf("decode AI grant rules: %w", err)
	}
	return rules, nil
}

func findAIGrantRule(rules []guard.GrantRule, id string) (guard.GrantRule, bool) {
	for _, rule := range rules {
		if rule.ID == id {
			return rule, true
		}
	}
	return guard.GrantRule{}, false
}

func aiGrantRuleKeyConflict(rules []guard.GrantRule, candidate guard.GrantRule) bool {
	for _, rule := range rules {
		if rule.ID != candidate.ID && rule.DeviceID == candidate.DeviceID && rule.Action == candidate.Action && rule.Path == candidate.Path {
			return true
		}
	}
	return false
}

func (m *aiSettingsMigration) migrateGrantRules() error {
	sourceRow, sourceFound := m.source(guard.GrantRulesSettingKey)
	targetKey, targetRow, targetFound := m.targetOf(guard.GrantRulesSettingKey)
	source, err := decodeAIGrantRules(sourceRow, sourceFound)
	if err != nil {
		return err
	}
	target, err := decodeAIGrantRules(targetRow, targetFound)
	if err != nil {
		return err
	}
	m.rules = target
	sort.SliceStable(source, func(i, j int) bool { return source[i].ID < source[j].ID })
	added := false
	for _, rule := range source {
		if rule.ID == "" {
			return errors.New("open-mode AI grant rule has an empty ID")
		}
		if m.tombstoned(guard.AIGrantSyncRulePrefix + rule.ID) {
			m.report.GrantRules.Tombstoned++
			continue
		}
		if existing, ok := findAIGrantRule(target, rule.ID); ok {
			if reflect.DeepEqual(existing, rule) {
				m.report.GrantRules.Existing++
			} else {
				m.report.GrantRules.Conflicts++
			}
			continue
		}
		if aiGrantRuleKeyConflict(target, rule) {
			m.report.GrantRules.Conflicts++
			continue
		}
		target = append(target, rule)
		m.report.GrantRules.Migrated++
		added = true
	}
	m.rules = target
	if !added {
		return nil
	}
	return m.writeMerged(targetKey, target, sourceRow, targetRow, targetFound)
}

func (m *aiSettingsMigration) targetActive(kind, id string) bool {
	switch kind {
	case store.SyncTombstoneKindAIProfile:
		_, ok := findAIProfile(m.profiles, id)
		return ok
	case aiPermissionTombstoneKind:
		_, row, found := m.targetOf(guard.PermissionSettingKey)
		return id == "global" && found && row.Value != ""
	case store.SyncTombstoneKindAIGrant:
		switch {
		case strings.HasPrefix(id, guard.AIGrantSyncDevicePrefix):
			_, ok := m.grants[strings.TrimPrefix(id, guard.AIGrantSyncDevicePrefix)]
			return ok
		case strings.HasPrefix(id, guard.AIGrantSyncRulePrefix):
			_, ok := findAIGrantRule(m.rules, strings.TrimPrefix(id, guard.AIGrantSyncRulePrefix))
			return ok
		case strings.HasPrefix(id, guard.AIGrantSyncModePrefix):
			_, ok := m.modes[strings.TrimPrefix(id, guard.AIGrantSyncModePrefix)]
			return ok
		}
	}
	return false
}

func (m *aiSettingsMigration) migrateTombstones() error {
	for _, row := range m.tombstones {
		if target, found := m.target[row.ID]; found {
			if target.Kind == row.Kind && target.DeletedAt == row.DeletedAt {
				m.report.Tombstones.Existing++
			} else {
				m.report.Tombstones.Conflicts++
			}
			continue
		}
		if m.targetActive(row.Kind, row.ID) {
			m.report.Tombstones.Conflicts++
			continue
		}
		row.OwnerID = m.ownerID
		if err := m.database.Create(&row).Error; err != nil {
			return err
		}
		m.target[row.ID] = row
		m.report.Tombstones.Migrated++
	}
	return nil
}
