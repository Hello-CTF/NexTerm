package legacy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	migrationVersion          = "global-ai-settings-owner-v1"
	aiPermissionTombstoneKind = "ai_permission"
)

var legacySettingKeys = []string{
	store.AIProfilesSettingKey,
	guard.PermissionSettingKey,
	guard.DeviceGrantsSettingKey,
	guard.GrantRulesSettingKey,
	guard.DevicePermissionSettingKey,
}

var legacyTombstoneKinds = []string{
	store.SyncTombstoneKindAIProfile,
	aiPermissionTombstoneKind,
	store.SyncTombstoneKindAIGrant,
}

type ItemReport struct {
	Migrated   int64
	Existing   int64
	Conflicts  int64
	Tombstoned int64
}

type Report struct {
	Profiles                  ItemReport
	Permissions               ItemReport
	DeviceGrants              ItemReport
	GrantRules                ItemReport
	DeviceModes               ItemReport
	Tombstones                ItemReport
	LedgerEntries             int64
	SkippedNoActiveSuperadmin bool
}

func (r Report) Total() ItemReport {
	var total ItemReport
	for _, item := range []ItemReport{r.Profiles, r.Permissions, r.DeviceGrants, r.GrantRules, r.DeviceModes, r.Tombstones} {
		total.Migrated += item.Migrated
		total.Existing += item.Existing
		total.Conflicts += item.Conflicts
		total.Tombstoned += item.Tombstoned
	}
	return total
}

func (r Report) Empty() bool {
	total := r.Total()
	return total == (ItemReport{}) && r.LedgerEntries == 0 && !r.SkippedNoActiveSuperadmin
}

// Migrate copies legacy global AI settings to the first active superadmin. The
// legacy rows remain intact for desktop/Open mode and as the original copy.
func Migrate(ctx context.Context, database *gorm.DB, shared bool) (Report, error) {
	var report Report
	if shared {
		return report, nil
	}
	err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pending, err := migrationPending(tx)
		if err != nil || !pending {
			return err
		}
		ownerID, err := firstActiveSuperadmin(ctx, tx)
		if err != nil {
			return err
		}
		if ownerID == "" {
			report.SkippedNoActiveSuperadmin = true
			return nil
		}
		migration, err := newMigration(ctx, tx, ownerID, &report)
		if err != nil {
			return err
		}
		if err := migration.migrateProfiles(); err != nil {
			return err
		}
		if err := migration.migratePermission(); err != nil {
			return err
		}
		if err := migration.migrateDeviceGrants(); err != nil {
			return err
		}
		if err := migration.migrateGrantRules(); err != nil {
			return err
		}
		if err := migration.migrateDeviceModes(); err != nil {
			return err
		}
		return migration.migrateTombstones()
	})
	if err != nil {
		return Report{}, fmt.Errorf("legacy AI settings ownership migration: %w", err)
	}
	return report, nil
}

func settingKeysIn(keys []string) clause.Expression {
	values := make([]any, len(keys))
	for index, key := range keys {
		values[index] = key
	}
	return clause.IN{Column: clause.Column{Name: "key"}, Values: values}
}

func migrationPending(database *gorm.DB) (bool, error) {
	var count int64
	if err := database.Model(&store.Setting{}).Where(settingKeysIn(legacySettingKeys)).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if err := database.Model(&store.SyncTombstone{}).
		Where("owner_id = '' AND kind IN ?", legacyTombstoneKinds).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func firstActiveSuperadmin(ctx context.Context, database *gorm.DB) (string, error) {
	var user store.AppUser
	err := database.WithContext(ctx).
		Select("id").
		Where("role = ? AND state = ?", "superadmin", "active").
		Order("created_at").Order("id").
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("select first active superadmin: %w", err)
	}
	return user.ID, nil
}

type profileState struct {
	Version  int                `json:"version"`
	Profiles []profiles.Profile `json:"profiles"`
	ActiveID *string            `json:"activeId"`
}

type ledgerKey struct {
	kind     string
	recordID string
}

type migration struct {
	ctx        context.Context
	database   *gorm.DB
	ownerID    string
	report     *Report
	settings   map[string]store.Setting
	ledger     map[ledgerKey]struct{}
	global     map[string]store.SyncTombstone
	target     map[string]store.SyncTombstone
	tombstones []store.SyncTombstone

	profiles profileState
	grants   map[string]guard.DeviceGrant
	rules    []guard.GrantRule
	modes    map[string]guard.Mode
}

func newMigration(ctx context.Context, database *gorm.DB, ownerID string, report *Report) (*migration, error) {
	targetKeys := []string{
		store.AIProfilesSettingKeyForUser(ownerID),
		guard.PermissionSettingKey + ".user." + ownerID,
		guard.DeviceGrantsSettingKey + ".user." + ownerID,
		guard.GrantRulesSettingKey + ".user." + ownerID,
		guard.DevicePermissionSettingKey + ".user." + ownerID,
	}
	keys := append(append([]string(nil), legacySettingKeys...), targetKeys...)
	var settingRows []store.Setting
	if err := database.WithContext(ctx).Where(settingKeysIn(keys)).Find(&settingRows).Error; err != nil {
		return nil, err
	}
	settings := make(map[string]store.Setting, len(settingRows))
	for _, row := range settingRows {
		settings[row.Key] = row
	}

	var ledgerRows []store.AIOwnerMigration
	if err := database.WithContext(ctx).Where("migration = ?", migrationVersion).Find(&ledgerRows).Error; err != nil {
		return nil, err
	}
	ledger := make(map[ledgerKey]struct{}, len(ledgerRows))
	for _, row := range ledgerRows {
		ledger[ledgerKey{kind: row.Kind, recordID: row.RecordID}] = struct{}{}
	}

	var tombstoneRows []store.SyncTombstone
	if err := database.WithContext(ctx).
		Where("owner_id IN ? AND kind IN ?", []string{"", ownerID}, legacyTombstoneKinds).
		Order("owner_id").Order("kind").Order("id").
		Find(&tombstoneRows).Error; err != nil {
		return nil, err
	}
	m := &migration{
		ctx: ctx, database: database, ownerID: ownerID, report: report,
		settings: settings, ledger: ledger,
		global: make(map[string]store.SyncTombstone), target: make(map[string]store.SyncTombstone),
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

func (m *migration) claim(kind, recordID string) (bool, error) {
	key := ledgerKey{kind: kind, recordID: recordID}
	if _, ok := m.ledger[key]; ok {
		return false, nil
	}
	result := m.database.WithContext(m.ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&store.AIOwnerMigration{
		Migration: migrationVersion,
		Kind:      kind,
		RecordID:  recordID,
		OwnerID:   m.ownerID,
		AddedAt:   ids.NowMS(),
	})
	if result.Error != nil {
		return false, result.Error
	}
	m.ledger[key] = struct{}{}
	if result.RowsAffected == 1 {
		m.report.LedgerEntries++
		return true, nil
	}
	return false, nil
}

func (m *migration) putSetting(key, value string, updatedAt int64) error {
	row := store.Setting{Key: key, Value: value, UpdatedAt: updatedAt}
	if err := m.database.WithContext(m.ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&row).Error; err != nil {
		return err
	}
	m.settings[key] = row
	return nil
}

func (m *migration) tombstoned(id string) bool {
	_, global := m.global[id]
	_, target := m.target[id]
	return global || target
}

func decodeProfiles(row store.Setting, found bool) (profileState, error) {
	if !found {
		return profileState{Version: profiles.StoreVersion, Profiles: []profiles.Profile{}}, nil
	}
	var state profileState
	if err := json.Unmarshal([]byte(row.Value), &state); err != nil {
		return profileState{}, fmt.Errorf("decode AI profiles: %w", err)
	}
	if state.Version <= 0 || state.Version > profiles.StoreVersion {
		return profileState{}, fmt.Errorf("unsupported AI profile store version %d", state.Version)
	}
	if state.Profiles == nil {
		state.Profiles = []profiles.Profile{}
	}
	return state, nil
}

func findProfile(state profileState, id string) (profiles.Profile, bool) {
	for _, profile := range state.Profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return profiles.Profile{}, false
}

func profileNameConflict(state profileState, candidate profiles.Profile) bool {
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

func validActiveProfile(state profileState) bool {
	return state.ActiveID != nil && func() bool {
		_, ok := findProfile(state, *state.ActiveID)
		return ok
	}()
}

func (m *migration) migrateProfiles() error {
	sourceRow, sourceFound := m.settings[store.AIProfilesSettingKey]
	targetKey := store.AIProfilesSettingKeyForUser(m.ownerID)
	targetRow, targetFound := m.settings[targetKey]
	source, err := decodeProfiles(sourceRow, sourceFound)
	if err != nil {
		return err
	}
	target, err := decodeProfiles(targetRow, targetFound)
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
			return errors.New("legacy AI profile has an empty ID")
		}
		claimed, err := m.claim("profile", profile.ID)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if m.tombstoned(profile.ID) {
			m.report.Profiles.Tombstoned++
			continue
		}
		if existing, ok := findProfile(target, profile.ID); ok {
			if reflect.DeepEqual(existing, profile) {
				m.report.Profiles.Existing++
			} else {
				m.report.Profiles.Conflicts++
			}
			continue
		}
		if profileNameConflict(target, profile) {
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
	if !validActiveProfile(target) {
		target.ActiveID = nil
		if source.ActiveID != nil && !m.tombstoned(*source.ActiveID) {
			if legacyActive, ok := findProfile(source, *source.ActiveID); ok {
				if mergedActive, ok := findProfile(target, *source.ActiveID); ok && reflect.DeepEqual(legacyActive, mergedActive) {
					target.ActiveID = source.ActiveID
				}
			}
		}
		if target.ActiveID == nil && len(target.Profiles) > 0 {
			activeID := target.Profiles[0].ID
			target.ActiveID = &activeID
		}
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		return err
	}
	updatedAt := sourceRow.UpdatedAt
	if targetFound {
		updatedAt = max(updatedAt, targetRow.UpdatedAt)
	}
	return m.putSetting(targetKey, string(encoded), updatedAt)
}

func (m *migration) migratePermission() error {
	sourceRow, sourceFound := m.settings[guard.PermissionSettingKey]
	if !sourceFound || sourceRow.Value == "" {
		return nil
	}
	claimed, err := m.claim("permission", "global")
	if err != nil || !claimed {
		return err
	}
	if m.tombstoned("global") {
		m.report.Permissions.Tombstoned++
		return nil
	}
	targetKey := guard.PermissionSettingKey + ".user." + m.ownerID
	targetRow, targetFound := m.settings[targetKey]
	if targetFound {
		var sourceConfig, targetConfig guard.Config
		if err := json.Unmarshal([]byte(sourceRow.Value), &sourceConfig); err != nil {
			return fmt.Errorf("decode legacy AI permission: %w", err)
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

func decodeDeviceGrants(row store.Setting, found bool) (map[string]guard.DeviceGrant, error) {
	result := make(map[string]guard.DeviceGrant)
	if !found || row.Value == "" {
		return result, nil
	}
	if err := json.Unmarshal([]byte(row.Value), &result); err != nil {
		return nil, fmt.Errorf("decode AI device grants: %w", err)
	}
	if result == nil {
		result = make(map[string]guard.DeviceGrant)
	}
	return result, nil
}

func (m *migration) migrateDeviceGrants() error {
	sourceRow, sourceFound := m.settings[guard.DeviceGrantsSettingKey]
	targetKey := guard.DeviceGrantsSettingKey + ".user." + m.ownerID
	targetRow, targetFound := m.settings[targetKey]
	source, err := decodeDeviceGrants(sourceRow, sourceFound)
	if err != nil {
		return err
	}
	target, err := decodeDeviceGrants(targetRow, targetFound)
	if err != nil {
		return err
	}
	m.grants = target
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	added := false
	for _, deviceID := range keys {
		grant := source[deviceID]
		claimed, err := m.claim("device_grant", deviceID)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if m.tombstoned(guard.AIGrantSyncDevicePrefix + deviceID) {
			m.report.DeviceGrants.Tombstoned++
			continue
		}
		if existing, ok := target[deviceID]; ok {
			if reflect.DeepEqual(existing, grant) {
				m.report.DeviceGrants.Existing++
			} else {
				m.report.DeviceGrants.Conflicts++
			}
			continue
		}
		target[deviceID] = grant
		m.report.DeviceGrants.Migrated++
		added = true
	}
	m.grants = target
	if !added {
		return nil
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		return err
	}
	updatedAt := sourceRow.UpdatedAt
	if targetFound {
		updatedAt = max(updatedAt, targetRow.UpdatedAt)
	}
	return m.putSetting(targetKey, string(encoded), updatedAt)
}

func decodeGrantRules(row store.Setting, found bool) ([]guard.GrantRule, error) {
	if !found || row.Value == "" {
		return []guard.GrantRule{}, nil
	}
	var rules []guard.GrantRule
	if err := json.Unmarshal([]byte(row.Value), &rules); err != nil {
		return nil, fmt.Errorf("decode AI grant rules: %w", err)
	}
	return rules, nil
}

func findRule(rules []guard.GrantRule, id string) (guard.GrantRule, bool) {
	for _, rule := range rules {
		if rule.ID == id {
			return rule, true
		}
	}
	return guard.GrantRule{}, false
}

func ruleKeyConflict(rules []guard.GrantRule, candidate guard.GrantRule) bool {
	for _, rule := range rules {
		if rule.ID != candidate.ID && rule.DeviceID == candidate.DeviceID && rule.Action == candidate.Action && rule.Path == candidate.Path {
			return true
		}
	}
	return false
}

func (m *migration) migrateGrantRules() error {
	sourceRow, sourceFound := m.settings[guard.GrantRulesSettingKey]
	targetKey := guard.GrantRulesSettingKey + ".user." + m.ownerID
	targetRow, targetFound := m.settings[targetKey]
	source, err := decodeGrantRules(sourceRow, sourceFound)
	if err != nil {
		return err
	}
	target, err := decodeGrantRules(targetRow, targetFound)
	if err != nil {
		return err
	}
	m.rules = target
	sort.SliceStable(source, func(i, j int) bool { return source[i].ID < source[j].ID })
	added := false
	for _, rule := range source {
		if rule.ID == "" {
			return errors.New("legacy AI grant rule has an empty ID")
		}
		claimed, err := m.claim("grant_rule", rule.ID)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if m.tombstoned(guard.AIGrantSyncRulePrefix + rule.ID) {
			m.report.GrantRules.Tombstoned++
			continue
		}
		if existing, ok := findRule(target, rule.ID); ok {
			if reflect.DeepEqual(existing, rule) {
				m.report.GrantRules.Existing++
			} else {
				m.report.GrantRules.Conflicts++
			}
			continue
		}
		if ruleKeyConflict(target, rule) {
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
	encoded, err := json.Marshal(target)
	if err != nil {
		return err
	}
	updatedAt := sourceRow.UpdatedAt
	if targetFound {
		updatedAt = max(updatedAt, targetRow.UpdatedAt)
	}
	return m.putSetting(targetKey, string(encoded), updatedAt)
}

func decodeDeviceModes(row store.Setting, found bool) (map[string]guard.Mode, error) {
	result := make(map[string]guard.Mode)
	if !found || row.Value == "" {
		return result, nil
	}
	if err := json.Unmarshal([]byte(row.Value), &result); err != nil {
		return nil, fmt.Errorf("decode AI device permission modes: %w", err)
	}
	if result == nil {
		result = make(map[string]guard.Mode)
	}
	return result, nil
}

func (m *migration) migrateDeviceModes() error {
	sourceRow, sourceFound := m.settings[guard.DevicePermissionSettingKey]
	targetKey := guard.DevicePermissionSettingKey + ".user." + m.ownerID
	targetRow, targetFound := m.settings[targetKey]
	source, err := decodeDeviceModes(sourceRow, sourceFound)
	if err != nil {
		return err
	}
	target, err := decodeDeviceModes(targetRow, targetFound)
	if err != nil {
		return err
	}
	m.modes = target
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	added := false
	for _, deviceID := range keys {
		mode := source[deviceID]
		claimed, err := m.claim("device_mode", deviceID)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if m.tombstoned(guard.AIGrantSyncModePrefix + deviceID) {
			m.report.DeviceModes.Tombstoned++
			continue
		}
		if existing, ok := target[deviceID]; ok {
			if existing == mode {
				m.report.DeviceModes.Existing++
			} else {
				m.report.DeviceModes.Conflicts++
			}
			continue
		}
		target[deviceID] = mode
		m.report.DeviceModes.Migrated++
		added = true
	}
	m.modes = target
	if !added {
		return nil
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		return err
	}
	updatedAt := sourceRow.UpdatedAt
	if targetFound {
		updatedAt = max(updatedAt, targetRow.UpdatedAt)
	}
	return m.putSetting(targetKey, string(encoded), updatedAt)
}

func tombstoneLedgerKind(kind string) string {
	return "tombstone_" + kind
}

func (m *migration) targetActive(kind, id string) bool {
	switch kind {
	case store.SyncTombstoneKindAIProfile:
		_, ok := findProfile(m.profiles, id)
		return ok
	case aiPermissionTombstoneKind:
		row, found := m.settings[guard.PermissionSettingKey+".user."+m.ownerID]
		return id == "global" && found && row.Value != ""
	case store.SyncTombstoneKindAIGrant:
		switch {
		case strings.HasPrefix(id, guard.AIGrantSyncDevicePrefix):
			_, ok := m.grants[strings.TrimPrefix(id, guard.AIGrantSyncDevicePrefix)]
			return ok
		case strings.HasPrefix(id, guard.AIGrantSyncRulePrefix):
			_, ok := findRule(m.rules, strings.TrimPrefix(id, guard.AIGrantSyncRulePrefix))
			return ok
		case strings.HasPrefix(id, guard.AIGrantSyncModePrefix):
			_, ok := m.modes[strings.TrimPrefix(id, guard.AIGrantSyncModePrefix)]
			return ok
		}
	}
	return false
}

func (m *migration) migrateTombstones() error {
	for _, row := range m.tombstones {
		claimed, err := m.claim(tombstoneLedgerKind(row.Kind), row.ID)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
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
		if err := m.database.WithContext(m.ctx).Create(&row).Error; err != nil {
			return err
		}
		m.target[row.ID] = row
		m.report.Tombstones.Migrated++
	}
	return nil
}
