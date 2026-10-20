package legacy_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner/legacy"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

const faultMigrationLedgerVersion = "global-ai-settings-owner-v1"

type ledgerEntryKey struct {
	kind     string
	recordID string
}

var (
	faultPMigrate        = profiles.Profile{ID: "p-migrate", Name: "legacy-profile", BaseURL: "https://legacy.invalid/v1", APIKey: legacySecret, Model: "legacy-model", ContextWindow: 123, Stream: true}
	faultPConflictSource = profiles.Profile{ID: "p-conflict", Name: "legacy-conflict", APIKey: "legacy-conflict-secret", Model: "legacy"}
	faultPConflictTarget = profiles.Profile{ID: "p-conflict", Name: "target-conflict", APIKey: targetSecret, Model: "target"}
	faultPSame           = profiles.Profile{ID: "p-same", Name: "same", Model: "same-model", Stream: true}
	faultPDeleted        = profiles.Profile{ID: "p-deleted", Name: "deleted", Model: "deleted"}
	faultPNameSource     = profiles.Profile{ID: "p-name-legacy", Name: "shared-name", Model: "legacy"}
	faultPTarget         = profiles.Profile{ID: "p-target", Name: "target", Model: "target"}
	faultPNameTarget     = profiles.Profile{ID: "p-name-target", Name: "shared-name", Model: "target"}
)

var (
	faultPermission   = guard.Config{Mode: guard.ReadOnly, DangerRules: []string{"legacy-danger"}}
	faultGrantsSource = map[string]guard.DeviceGrant{
		"device-new":      testGrant("device-new", guard.GrantKindTerminalWrite, 101),
		"device-conflict": testGrant("device-conflict", guard.GrantKindSessionExec, 102),
		"device-same":     testGrant("device-same", guard.GrantKindTerminalWrite, 103),
		"device-deleted":  testGrant("device-deleted", guard.GrantKindTerminalWrite, 104),
	}
	faultGrantsTarget = map[string]guard.DeviceGrant{
		"device-conflict": testGrant("device-conflict", guard.GrantKindTerminalWrite, 202),
		"device-same":     testGrant("device-same", guard.GrantKindTerminalWrite, 103),
	}
	faultRulesSource = []guard.GrantRule{
		testRule("rule-new", "/new/**"),
		testRule("rule-conflict", "/legacy/**"),
		testRule("rule-same", "/same/**"),
		testRule("rule-deleted", "/deleted/**"),
	}
	faultRulesTarget = []guard.GrantRule{
		{ID: "rule-conflict", DeviceID: "asset-a", Action: "write_file", Path: "/target/**", CreatedAt: 30, UpdatedAt: 40},
		testRule("rule-same", "/same/**"),
	}
	faultModesSource = map[string]guard.Mode{
		"device-new": guard.Silent, "device-conflict": guard.ReadOnly, "device-same": guard.ReadWrite, "device-deleted": guard.Silent,
	}
	faultModesTarget = map[string]guard.Mode{"device-conflict": guard.Unattended, "device-same": guard.ReadWrite}
)

var faultTombstones = []struct {
	id, kind  string
	deletedAt int64
}{
	{"p-deleted", store.SyncTombstoneKindAIProfile, 301},
	{"p-orphan", store.SyncTombstoneKindAIProfile, 302},
	{guard.AIGrantSyncDevicePrefix + "device-deleted", store.SyncTombstoneKindAIGrant, 303},
	{guard.AIGrantSyncModePrefix + "device-deleted", store.SyncTombstoneKindAIGrant, 304},
	{guard.AIGrantSyncRulePrefix + "rule-deleted", store.SyncTombstoneKindAIGrant, 305},
}

var faultMigrationExpectedReport = legacy.Report{
	Profiles:      legacy.ItemReport{Migrated: 1, Existing: 1, Conflicts: 2, Tombstoned: 1},
	Permissions:   legacy.ItemReport{Migrated: 1},
	DeviceGrants:  legacy.ItemReport{Migrated: 1, Existing: 1, Conflicts: 1, Tombstoned: 1},
	GrantRules:    legacy.ItemReport{Migrated: 1, Existing: 1, Conflicts: 1, Tombstoned: 1},
	DeviceModes:   legacy.ItemReport{Migrated: 1, Existing: 1, Conflicts: 1, Tombstoned: 1},
	Tombstones:    legacy.ItemReport{Migrated: int64(len(faultTombstones))},
	LedgerEntries: 23,
}

func seedFaultMigration(t *testing.T, database *store.Store) {
	t.Helper()
	putUser(t, database, laterAdmin, "superadmin", "active", 20)
	putUser(t, database, firstAdmin, "superadmin", "active", 10)
	putJSONSetting(t, database, store.AIProfilesSettingKey, profileJSON(stringPointer(faultPMigrate.ID), faultPMigrate, faultPConflictSource, faultPSame, faultPDeleted, faultPNameSource), 100)
	putJSONSetting(t, database, store.AIProfilesSettingKeyForUser(firstAdmin), profileJSON(stringPointer(faultPTarget.ID), faultPTarget, faultPConflictTarget, faultPSame, faultPNameTarget), 200)
	putJSONSetting(t, database, guard.PermissionSettingKey, faultPermission, 100)
	putJSONSetting(t, database, guard.DeviceGrantsSettingKey, faultGrantsSource, 100)
	putJSONSetting(t, database, guard.DeviceGrantsSettingKey+".user."+firstAdmin, faultGrantsTarget, 200)
	putJSONSetting(t, database, guard.GrantRulesSettingKey, faultRulesSource, 100)
	putJSONSetting(t, database, guard.GrantRulesSettingKey+".user."+firstAdmin, faultRulesTarget, 200)
	putJSONSetting(t, database, guard.DevicePermissionSettingKey, faultModesSource, 100)
	putJSONSetting(t, database, guard.DevicePermissionSettingKey+".user."+firstAdmin, faultModesTarget, 200)
	for _, tombstone := range faultTombstones {
		putTombstone(t, database, "", tombstone.id, tombstone.kind, tombstone.deletedAt)
	}
}

func faultSourceSettings(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		store.AIProfilesSettingKey:       mustJSON(t, profileJSON(stringPointer(faultPMigrate.ID), faultPMigrate, faultPConflictSource, faultPSame, faultPDeleted, faultPNameSource)),
		guard.PermissionSettingKey:       mustJSON(t, faultPermission),
		guard.DeviceGrantsSettingKey:     mustJSON(t, faultGrantsSource),
		guard.GrantRulesSettingKey:       mustJSON(t, faultRulesSource),
		guard.DevicePermissionSettingKey: mustJSON(t, faultModesSource),
	}
}

func faultExpectedLedger() map[ledgerEntryKey]struct{} {
	keys := make(map[ledgerEntryKey]struct{})
	for _, profile := range []profiles.Profile{faultPMigrate, faultPConflictSource, faultPSame, faultPDeleted, faultPNameSource} {
		keys[ledgerEntryKey{kind: "profile", recordID: profile.ID}] = struct{}{}
	}
	keys[ledgerEntryKey{kind: "permission", recordID: "global"}] = struct{}{}
	for deviceID := range faultGrantsSource {
		keys[ledgerEntryKey{kind: "device_grant", recordID: deviceID}] = struct{}{}
		keys[ledgerEntryKey{kind: "device_mode", recordID: deviceID}] = struct{}{}
	}
	for _, rule := range faultRulesSource {
		keys[ledgerEntryKey{kind: "grant_rule", recordID: rule.ID}] = struct{}{}
	}
	for _, tombstone := range faultTombstones {
		keys[ledgerEntryKey{kind: "tombstone_" + tombstone.kind, recordID: tombstone.id}] = struct{}{}
	}
	return keys
}

func verifyFaultMigrationOutcome(t *testing.T, database *store.Store, report legacy.Report) {
	t.Helper()
	if report != faultMigrationExpectedReport {
		t.Fatalf("migration report = %+v, want %+v", report, faultMigrationExpectedReport)
	}
	for key, expected := range faultSourceSettings(t) {
		row, found := readSetting(t, database, key)
		if !found || row.Value != expected || row.UpdatedAt != 100 {
			t.Fatalf("legacy setting %s was modified: %+v, found = %v", key, row, found)
		}
	}
	if _, found := readSetting(t, database, store.AIProfilesSettingKeyForUser(laterAdmin)); found {
		t.Fatal("legacy profiles were assigned to a later superadmin")
	}
	targetProfiles := readProfiles(t, database, store.AIProfilesSettingKeyForUser(firstAdmin))
	if targetProfiles.ActiveID == nil || *targetProfiles.ActiveID != faultPTarget.ID || len(targetProfiles.Profiles) != 5 {
		t.Fatalf("target active profile or merged profile count changed: %+v", targetProfiles)
	}
	if profile, ok := findTestProfile(targetProfiles, faultPMigrate.ID); !ok || !reflect.DeepEqual(profile, faultPMigrate) {
		t.Fatal("legacy profile was not copied verbatim")
	}
	if profile, ok := findTestProfile(targetProfiles, faultPConflictTarget.ID); !ok || profile.APIKey != targetSecret || profile.Name != faultPConflictTarget.Name {
		t.Fatal("legacy profile overwrote the target profile")
	}
	for _, id := range []string{faultPDeleted.ID, faultPNameSource.ID} {
		if _, ok := findTestProfile(targetProfiles, id); ok {
			t.Fatalf("profile %s must not appear in the target scope", id)
		}
	}
	permissionRow, found := readSetting(t, database, guard.PermissionSettingKey+".user."+firstAdmin)
	if !found || permissionRow.UpdatedAt != 100 || permissionRow.Value != mustJSON(t, faultPermission) {
		t.Fatal("permission migration did not copy the legacy config verbatim")
	}
	var ownerless, owned int64
	if err := database.GORM().Model(&store.SyncTombstone{}).Where("owner_id = ''").Count(&ownerless).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Model(&store.SyncTombstone{}).Where("owner_id = ?", firstAdmin).Count(&owned).Error; err != nil {
		t.Fatal(err)
	}
	if ownerless != int64(len(faultTombstones)) || owned != int64(len(faultTombstones)) {
		t.Fatalf("tombstone counts ownerless = %d, target = %d", ownerless, owned)
	}
	for _, tombstone := range faultTombstones {
		var globalRow, ownedRow store.SyncTombstone
		if err := database.GORM().Where("owner_id = ? AND id = ? AND kind = ?", "", tombstone.id, tombstone.kind).First(&globalRow).Error; err != nil {
			t.Fatalf("global tombstone %s is missing: %v", tombstone.id, err)
		}
		if err := database.GORM().Where("owner_id = ? AND id = ?", firstAdmin, tombstone.id).First(&ownedRow).Error; err != nil {
			t.Fatalf("migrated tombstone %s is missing: %v", tombstone.id, err)
		}
		if ownedRow.Kind != tombstone.kind || ownedRow.DeletedAt != tombstone.deletedAt {
			t.Fatalf("migrated tombstone %s = %+v, want kind %s deletedAt %d", tombstone.id, ownedRow, tombstone.kind, tombstone.deletedAt)
		}
	}
	var ledgerRows []store.AIOwnerMigration
	if err := database.GORM().Where("migration = ?", faultMigrationLedgerVersion).Find(&ledgerRows).Error; err != nil {
		t.Fatal(err)
	}
	expectedLedger := faultExpectedLedger()
	if len(ledgerRows) != len(expectedLedger) {
		t.Fatalf("ledger entries = %d, want %d", len(ledgerRows), len(expectedLedger))
	}
	seen := make(map[ledgerEntryKey]struct{}, len(ledgerRows))
	for _, row := range ledgerRows {
		if row.OwnerID != firstAdmin {
			t.Fatalf("ledger entry %+v has owner %q", row, row.OwnerID)
		}
		if row.AddedAt <= 0 {
			t.Fatalf("ledger entry %+v has no added-at timestamp", row)
		}
		seen[ledgerEntryKey{kind: row.Kind, recordID: row.RecordID}] = struct{}{}
	}
	for key := range expectedLedger {
		if _, ok := seen[key]; !ok {
			t.Fatalf("ledger entry %+v is missing", key)
		}
	}
}

// armMigrationWriteFault registers a create callback that trips on the hits-th
// write to the given table. The returned disarm func removes the callback and
// is safe to call more than once.
func armMigrationWriteFault(t *testing.T, database *store.Store, table string, hits int, match func(any) bool, trip func(*gorm.DB)) (disarm func()) {
	t.Helper()
	remaining := hits
	callbackName := "nexterm_test:ai_owner_migration_write_fault"
	if err := database.GORM().Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if remaining <= 0 || tx.Statement.Table != table {
			return
		}
		if match != nil && !match(statementDestValue(tx.Statement.Dest)) {
			return
		}
		remaining--
		if remaining == 0 {
			trip(tx)
		}
	}); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	disarm = func() {
		once.Do(func() { _ = database.GORM().Callback().Create().Remove(callbackName) })
	}
	t.Cleanup(disarm)
	return disarm
}

func statementDestValue(dest any) any {
	value := reflect.ValueOf(dest)
	if value.Kind() == reflect.Pointer && !value.IsNil() {
		return value.Elem().Interface()
	}
	return dest
}

func faultSettingKey(key string) func(any) bool {
	return func(value any) bool {
		row, ok := value.(store.Setting)
		return ok && row.Key == key
	}
}

func faultLedgerRecord(kind, recordID string) func(any) bool {
	return func(value any) bool {
		row, ok := value.(store.AIOwnerMigration)
		return ok && row.Kind == kind && row.RecordID == recordID
	}
}

func zeroLedgerAddedAt(snapshot databaseSnapshot) databaseSnapshot {
	normalized := databaseSnapshot{
		settings:   append([]store.Setting(nil), snapshot.settings...),
		tombstones: append([]store.SyncTombstone(nil), snapshot.tombstones...),
		ledger:     append([]store.AIOwnerMigration(nil), snapshot.ledger...),
	}
	for index := range normalized.ledger {
		normalized.ledger[index].AddedAt = 0
	}
	return normalized
}

func snapshotDifference(got, want databaseSnapshot) string {
	if len(got.settings) != len(want.settings) {
		return fmt.Sprintf("settings count = %d, want %d\ngot: %+v\nwant: %+v", len(got.settings), len(want.settings), got.settings, want.settings)
	}
	for index := range got.settings {
		if got.settings[index] != want.settings[index] {
			return fmt.Sprintf("setting row %d = %+v, want %+v", index, got.settings[index], want.settings[index])
		}
	}
	if len(got.tombstones) != len(want.tombstones) {
		return fmt.Sprintf("tombstone count = %d, want %d\ngot: %+v\nwant: %+v", len(got.tombstones), len(want.tombstones), got.tombstones, want.tombstones)
	}
	for index := range got.tombstones {
		if got.tombstones[index] != want.tombstones[index] {
			return fmt.Sprintf("tombstone row %d = %+v, want %+v", index, got.tombstones[index], want.tombstones[index])
		}
	}
	if len(got.ledger) != len(want.ledger) {
		return fmt.Sprintf("ledger count = %d, want %d\ngot: %+v\nwant: %+v", len(got.ledger), len(want.ledger), got.ledger, want.ledger)
	}
	for index := range got.ledger {
		if got.ledger[index] != want.ledger[index] {
			return fmt.Sprintf("ledger row %d = %+v, want %+v", index, got.ledger[index], want.ledger[index])
		}
	}
	return "snapshots differ"
}

// TestMigrateLegacyAISettingsWriteFaultDatabase injects direct write faults
// into every migration stage and verifies that a mid-migration failure rolls
// the whole transaction back and that a rerun after the crash converges to
// the same source, target, tombstone, and ledger state as an uninterrupted
// migration.
func TestMigrateLegacyAISettingsWriteFaultDatabase(t *testing.T) {
	baselineStore := dbtest.NewFixture(t).OpenStore(t)
	seedFaultMigration(t, baselineStore)
	baselineReport, err := legacy.Migrate(context.Background(), baselineStore.GORM(), false)
	if err != nil {
		t.Fatal(err)
	}
	verifyFaultMigrationOutcome(t, baselineStore, baselineReport)
	baseline := snapshotDatabase(t, baselineStore)

	scenarios := []struct {
		name  string
		table string
		hits  int
		match func(any) bool
		crash bool
	}{
		{"ProfileTargetUpsert", "setting", 1, faultSettingKey(store.AIProfilesSettingKeyForUser(firstAdmin)), false},
		{"PermissionTargetUpsert", "setting", 1, faultSettingKey(guard.PermissionSettingKey + ".user." + firstAdmin), false},
		{"DeviceGrantsTargetUpsert", "setting", 1, faultSettingKey(guard.DeviceGrantsSettingKey + ".user." + firstAdmin), false},
		{"GrantRulesTargetUpsert", "setting", 1, faultSettingKey(guard.GrantRulesSettingKey + ".user." + firstAdmin), false},
		{"DeviceModesTargetUpsert", "setting", 1, faultSettingKey(guard.DevicePermissionSettingKey + ".user." + firstAdmin), false},
		{"GrantRuleLedgerClaim", "ai_owner_migration", 1, faultLedgerRecord("grant_rule", "rule-new"), false},
		{"TombstoneCreate", "sync_tombstone", 3, nil, false},
		{"TombstoneLedgerClaim", "ai_owner_migration", 1, faultLedgerRecord("tombstone_"+store.SyncTombstoneKindAIProfile, "p-deleted"), false},
		{"CrashContextCancel", "setting", 1, faultSettingKey(guard.DeviceGrantsSettingKey + ".user." + firstAdmin), true},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			database := dbtest.NewFixture(t).OpenStore(t)
			seedFaultMigration(t, database)
			before := snapshotDatabase(t, database)

			ctx := context.Background()
			trip := func(tx *gorm.DB) { tx.AddError(errors.New("forced AI owner migration write failure")) }
			if scenario.crash {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				trip = func(*gorm.DB) { cancel() }
			}
			disarm := armMigrationWriteFault(t, database, scenario.table, scenario.hits, scenario.match, trip)

			report, err := legacy.Migrate(ctx, database.GORM(), false)
			disarm()
			if err == nil {
				t.Fatal("faulted migration succeeded")
			}
			if report != (legacy.Report{}) {
				t.Fatalf("faulted migration report = %+v, want zero", report)
			}
			if after := snapshotDatabase(t, database); !reflect.DeepEqual(before, after) {
				t.Fatalf("faulted migration was not fully rolled back: %s", snapshotDifference(after, before))
			}

			rerun, err := legacy.Migrate(context.Background(), database.GORM(), false)
			if err != nil {
				t.Fatalf("crash rerun failed: %v", err)
			}
			verifyFaultMigrationOutcome(t, database, rerun)
			if after := zeroLedgerAddedAt(snapshotDatabase(t, database)); !reflect.DeepEqual(after, zeroLedgerAddedAt(baseline)) {
				t.Fatalf("crash rerun did not converge to the uninterrupted baseline: %s", snapshotDifference(after, zeroLedgerAddedAt(baseline)))
			}

			repeated, err := legacy.Migrate(context.Background(), database.GORM(), false)
			if err != nil || !repeated.Empty() {
				t.Fatalf("idempotent reentry report = %+v, err = %v", repeated, err)
			}
			if after := zeroLedgerAddedAt(snapshotDatabase(t, database)); !reflect.DeepEqual(after, zeroLedgerAddedAt(baseline)) {
				t.Fatalf("idempotent reentry changed database rows: %s", snapshotDifference(after, zeroLedgerAddedAt(baseline)))
			}
		})
	}
}
