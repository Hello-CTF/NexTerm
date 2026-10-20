package legacy_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner/legacy"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	firstAdmin   = "first-admin"
	laterAdmin   = "later-admin"
	legacySecret = "legacy-secret-do-not-log"
	targetSecret = "target-secret-do-not-log"
)

type testProfileState struct {
	Version  int                `json:"version"`
	Profiles []profiles.Profile `json:"profiles"`
	ActiveID *string            `json:"activeId"`
}

type databaseSnapshot struct {
	settings   []store.Setting
	tombstones []store.SyncTombstone
	ledger     []store.AIOwnerMigration
}

func putUser(t *testing.T, database *store.Store, id, role, state string, createdAt int64) {
	t.Helper()
	if err := database.GORM().Create(&store.AppUser{
		ID: id, Username: id, Role: role, State: state, PasswordHash: "test", CreatedAt: createdAt, UpdatedAt: createdAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func putSetting(t *testing.T, database *store.Store, key, value string, updatedAt int64) {
	t.Helper()
	if err := database.GORM().Create(&store.Setting{Key: key, Value: value, UpdatedAt: updatedAt}).Error; err != nil {
		t.Fatal(err)
	}
}

func putJSONSetting(t *testing.T, database *store.Store, key string, value any, updatedAt int64) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	putSetting(t, database, key, string(encoded), updatedAt)
}

func putTombstone(t *testing.T, database *store.Store, ownerID, id, kind string, deletedAt int64) {
	t.Helper()
	if err := database.GORM().Create(&store.SyncTombstone{OwnerID: ownerID, ID: id, Kind: kind, DeletedAt: deletedAt}).Error; err != nil {
		t.Fatal(err)
	}
}

func readSetting(t *testing.T, database *store.Store, key string) (store.Setting, bool) {
	t.Helper()
	var row store.Setting
	err := database.GORM().Where(&store.Setting{Key: key}).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.Setting{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return row, true
}

func snapshotDatabase(t *testing.T, database *store.Store) databaseSnapshot {
	t.Helper()
	var snapshot databaseSnapshot
	if err := database.GORM().Order(clause.Column{Name: "key"}).Find(&snapshot.settings).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Order("owner_id").Order("id").Find(&snapshot.tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Order("migration").Order("kind").Order("record_id").Find(&snapshot.ledger).Error; err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func profileJSON(activeID *string, values ...profiles.Profile) testProfileState {
	return testProfileState{Version: profiles.StoreVersion, Profiles: values, ActiveID: activeID}
}

func stringPointer(value string) *string { return &value }

func testGrant(deviceID string, kind guard.GrantKind, updatedAt int64) guard.DeviceGrant {
	return guard.DeviceGrant{DeviceID: deviceID, Kinds: []guard.GrantKind{kind}, UpdatedAt: updatedAt}
}

func testRule(id, path string) guard.GrantRule {
	return guard.GrantRule{ID: id, DeviceID: "asset-a", Action: "write_file", Path: path, CreatedAt: 10, UpdatedAt: 20}
}

func findTestProfile(state testProfileState, id string) (profiles.Profile, bool) {
	for _, profile := range state.Profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return profiles.Profile{}, false
}

func readProfiles(t *testing.T, database *store.Store, key string) testProfileState {
	t.Helper()
	row, found := readSetting(t, database, key)
	if !found {
		t.Fatal("profile setting is missing")
	}
	var state testProfileState
	if err := json.Unmarshal([]byte(row.Value), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestMigrateLegacyAISettingsDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	putUser(t, database, "disabled-admin", "superadmin", "disabled", 1)
	putUser(t, database, laterAdmin, "superadmin", "active", 20)
	putUser(t, database, firstAdmin, "superadmin", "active", 10)

	pMigrate := profiles.Profile{ID: "p-migrate", Name: "legacy-profile", BaseURL: "https://legacy.invalid/v1", APIKey: legacySecret, Model: "legacy-model", ContextWindow: 123, Stream: true}
	pConflictSource := profiles.Profile{ID: "p-conflict", Name: "legacy-conflict", APIKey: "legacy-conflict-secret", Model: "legacy"}
	pConflictTarget := profiles.Profile{ID: "p-conflict", Name: "target-conflict", APIKey: targetSecret, Model: "target"}
	pSame := profiles.Profile{ID: "p-same", Name: "same", Model: "same-model", Stream: true}
	pDeleted := profiles.Profile{ID: "p-deleted", Name: "deleted", Model: "deleted"}
	pTargetDeleted := profiles.Profile{ID: "p-target-deleted", Name: "target-deleted", Model: "deleted"}
	pNameSource := profiles.Profile{ID: "p-name-legacy", Name: "shared-name", Model: "legacy"}
	pTarget := profiles.Profile{ID: "p-target", Name: "target", Model: "target"}
	pNameTarget := profiles.Profile{ID: "p-name-target", Name: "shared-name", Model: "target"}
	putJSONSetting(t, database, store.AIProfilesSettingKey, profileJSON(stringPointer(pMigrate.ID), pMigrate, pConflictSource, pSame, pDeleted, pTargetDeleted, pNameSource), 100)
	putJSONSetting(t, database, store.AIProfilesSettingKeyForUser(firstAdmin), profileJSON(stringPointer(pTarget.ID), pTarget, pConflictTarget, pSame, pNameTarget), 200)

	permission := guard.Config{Mode: guard.ReadOnly, DangerRules: []string{"legacy-danger"}}
	putJSONSetting(t, database, guard.PermissionSettingKey, permission, 100)

	grantsSource := map[string]guard.DeviceGrant{
		"device-new":            testGrant("device-new", guard.GrantKindTerminalWrite, 101),
		"device-conflict":       testGrant("device-conflict", guard.GrantKindSessionExec, 102),
		"device-same":           testGrant("device-same", guard.GrantKindTerminalWrite, 103),
		"device-deleted":        testGrant("device-deleted", guard.GrantKindTerminalWrite, 104),
		"device-target-deleted": testGrant("device-target-deleted", guard.GrantKindSessionExec, 105),
	}
	grantsTarget := map[string]guard.DeviceGrant{
		"device-conflict": testGrant("device-conflict", guard.GrantKindTerminalWrite, 202),
		"device-same":     testGrant("device-same", guard.GrantKindTerminalWrite, 103),
	}
	putJSONSetting(t, database, guard.DeviceGrantsSettingKey, grantsSource, 100)
	putJSONSetting(t, database, guard.DeviceGrantsSettingKey+".user."+firstAdmin, grantsTarget, 200)

	rulesSource := []guard.GrantRule{
		testRule("rule-new", "/new/**"),
		testRule("rule-conflict", "/legacy/**"),
		testRule("rule-same", "/same/**"),
		testRule("rule-deleted", "/deleted/**"),
		testRule("rule-target-deleted", "/target-deleted/**"),
	}
	rulesTarget := []guard.GrantRule{
		{ID: "rule-conflict", DeviceID: "asset-a", Action: "write_file", Path: "/target/**", CreatedAt: 30, UpdatedAt: 40},
		testRule("rule-same", "/same/**"),
	}
	putJSONSetting(t, database, guard.GrantRulesSettingKey, rulesSource, 100)
	putJSONSetting(t, database, guard.GrantRulesSettingKey+".user."+firstAdmin, rulesTarget, 200)

	modesSource := map[string]guard.Mode{
		"device-new": guard.Silent, "device-conflict": guard.ReadOnly, "device-same": guard.ReadWrite,
		"device-deleted": guard.Silent, "device-target-deleted": guard.ReadOnly,
	}
	modesTarget := map[string]guard.Mode{"device-conflict": guard.Unattended, "device-same": guard.ReadWrite}
	putJSONSetting(t, database, guard.DevicePermissionSettingKey, modesSource, 100)
	putJSONSetting(t, database, guard.DevicePermissionSettingKey+".user."+firstAdmin, modesTarget, 200)

	putTombstone(t, database, "", pDeleted.ID, store.SyncTombstoneKindAIProfile, 301)
	putTombstone(t, database, "", pTarget.ID, store.SyncTombstoneKindAIProfile, 302)
	putTombstone(t, database, "", "p-orphan", store.SyncTombstoneKindAIProfile, 303)
	putTombstone(t, database, firstAdmin, pTargetDeleted.ID, store.SyncTombstoneKindAIProfile, 304)
	putTombstone(t, database, "", "device/device-deleted", store.SyncTombstoneKindAIGrant, 305)
	putTombstone(t, database, "", "device/device-target-deleted", store.SyncTombstoneKindAIGrant, 306)
	putTombstone(t, database, firstAdmin, "device/device-target-deleted", store.SyncTombstoneKindAIGrant, 306)
	putTombstone(t, database, "", "rule/rule-deleted", store.SyncTombstoneKindAIGrant, 307)
	putTombstone(t, database, firstAdmin, "rule/rule-target-deleted", store.SyncTombstoneKindAIGrant, 308)
	putTombstone(t, database, "", "mode/device-deleted", store.SyncTombstoneKindAIGrant, 309)
	putTombstone(t, database, firstAdmin, "mode/device-target-deleted", store.SyncTombstoneKindAIGrant, 310)

	report, err := legacy.Migrate(ctx, database.GORM(), false)
	if err != nil {
		t.Fatal(err)
	}
	expected := legacy.Report{
		Profiles:      legacy.ItemReport{Migrated: 1, Existing: 1, Conflicts: 2, Tombstoned: 2},
		Permissions:   legacy.ItemReport{Migrated: 1},
		DeviceGrants:  legacy.ItemReport{Migrated: 1, Existing: 1, Conflicts: 1, Tombstoned: 2},
		GrantRules:    legacy.ItemReport{Migrated: 1, Existing: 1, Conflicts: 1, Tombstoned: 2},
		DeviceModes:   legacy.ItemReport{Migrated: 1, Existing: 1, Conflicts: 1, Tombstoned: 2},
		Tombstones:    legacy.ItemReport{Migrated: 5, Existing: 1, Conflicts: 1},
		LedgerEntries: 29,
	}
	if report != expected {
		t.Fatalf("migration report = %+v, want %+v", report, expected)
	}

	targetProfilesRow, found := readSetting(t, database, store.AIProfilesSettingKeyForUser(firstAdmin))
	if !found || targetProfilesRow.UpdatedAt != 200 {
		t.Fatalf("target profile revision = %d, found = %v", targetProfilesRow.UpdatedAt, found)
	}
	targetProfiles := readProfiles(t, database, store.AIProfilesSettingKeyForUser(firstAdmin))
	if targetProfiles.ActiveID == nil || *targetProfiles.ActiveID != pTarget.ID || len(targetProfiles.Profiles) != 5 {
		t.Fatal("target active profile or merged profile count changed unexpectedly")
	}
	if profile, ok := findTestProfile(targetProfiles, pConflictTarget.ID); !ok || profile.APIKey != targetSecret || profile.Name != pConflictTarget.Name {
		t.Fatal("legacy profile overwrote the target profile")
	}
	if _, ok := findTestProfile(targetProfiles, pNameSource.ID); ok {
		t.Fatal("same-name legacy profile was duplicated")
	}
	for _, id := range []string{pDeleted.ID, pTargetDeleted.ID} {
		if _, ok := findTestProfile(targetProfiles, id); ok {
			t.Fatalf("tombstoned profile %s was resurrected", id)
		}
	}
	if profile, ok := findTestProfile(targetProfiles, pMigrate.ID); !ok || profile.APIKey != legacySecret {
		t.Fatal("legacy profile was not copied verbatim")
	}
	if _, found := readSetting(t, database, store.AIProfilesSettingKeyForUser(laterAdmin)); found {
		t.Fatal("legacy profiles were assigned to a later superadmin")
	}
	permissionRow, found := readSetting(t, database, guard.PermissionSettingKey+".user."+firstAdmin)
	if !found || permissionRow.UpdatedAt != 100 {
		t.Fatal("permission migration did not preserve the legacy revision")
	}

	for key, expectedValue := range map[string]string{
		store.AIProfilesSettingKey:       mustJSON(t, profileJSON(stringPointer(pMigrate.ID), pMigrate, pConflictSource, pSame, pDeleted, pTargetDeleted, pNameSource)),
		guard.PermissionSettingKey:       mustJSON(t, permission),
		guard.DeviceGrantsSettingKey:     mustJSON(t, grantsSource),
		guard.GrantRulesSettingKey:       mustJSON(t, rulesSource),
		guard.DevicePermissionSettingKey: mustJSON(t, modesSource),
	} {
		row, found := readSetting(t, database, key)
		if !found || row.Value != expectedValue || row.UpdatedAt != 100 {
			t.Fatalf("legacy setting %s was modified", key)
		}
	}

	var ownerlessTombstones, targetTombstones int64
	if err := database.GORM().Model(&store.SyncTombstone{}).Where("owner_id = ''").Count(&ownerlessTombstones).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Model(&store.SyncTombstone{}).Where("owner_id = ?", firstAdmin).Count(&targetTombstones).Error; err != nil {
		t.Fatal(err)
	}
	if ownerlessTombstones != 7 || targetTombstones != 9 {
		t.Fatalf("tombstone counts ownerless = %d, target = %d", ownerlessTombstones, targetTombstones)
	}
	var targetActiveTombstone int64
	if err := database.GORM().Model(&store.SyncTombstone{}).Where("owner_id = ? AND id = ?", firstAdmin, pTarget.ID).Count(&targetActiveTombstone).Error; err != nil {
		t.Fatal(err)
	}
	if targetActiveTombstone != 0 {
		t.Fatal("legacy tombstone overrode an active target profile")
	}

	firstSnapshot := snapshotDatabase(t, database)
	repeated, err := legacy.Migrate(ctx, database.GORM(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.Empty() {
		t.Fatalf("idempotent reentry report = %+v", repeated)
	}
	if secondSnapshot := snapshotDatabase(t, database); !reflect.DeepEqual(firstSnapshot, secondSnapshot) {
		t.Fatal("migration reentry changed values, timestamps, tombstones, or ledger rows")
	}

	profileManager, err := profiles.NewManager(ctx, database, profiles.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	adminCtx := ipc.WithUserID(ctx, firstAdmin)
	otherCtx := ipc.WithUserID(ctx, laterAdmin)
	overview, err := profileManager.Overview(adminCtx)
	if err != nil || len(overview.Profiles) != 5 {
		t.Fatalf("admin runtime profile count = %d, err = %v", len(overview.Profiles), err)
	}
	otherOverview, err := profileManager.Overview(otherCtx)
	if err != nil || len(otherOverview.Profiles) != 0 {
		t.Fatalf("other account read legacy profiles: count = %d, err = %v", len(otherOverview.Profiles), err)
	}
	if _, err := profileManager.Overview(ctx); err == nil {
		t.Fatal("ownerless account runtime read a global or owner profile")
	}
	permissionManager, err := guard.NewManager(ctx, database, guard.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	adminPermission, err := permissionManager.Snapshot(adminCtx)
	if err != nil || adminPermission.Mode != guard.ReadOnly {
		t.Fatal("admin runtime did not read the migrated permission")
	}
	otherPermission, err := permissionManager.Snapshot(otherCtx)
	if err != nil || otherPermission.Mode != guard.ReadWrite {
		t.Fatal("other account inherited the legacy permission")
	}
	grantsManager, err := guard.NewGrants(ctx, database, nil, guard.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := grantsManager.Get(adminCtx, "device-new"); err != nil || !found {
		t.Fatal("admin runtime did not read the migrated grant")
	}
	if grants, err := grantsManager.List(otherCtx); err != nil || len(grants) != 0 {
		t.Fatal("other account inherited legacy grants")
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestMigrateLegacyAISettingsDefersWithoutSuperadminDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	putUser(t, database, "regular", "user", "active", 1)
	putUser(t, database, "disabled", "superadmin", "disabled", 2)
	putJSONSetting(t, database, store.AIProfilesSettingKey, profileJSON(nil, profiles.Profile{ID: "p-wait", Name: "wait", Model: "m"}), 100)
	putTombstone(t, database, "", "p-orphan", store.SyncTombstoneKindAIProfile, 200)
	before := snapshotDatabase(t, database)
	report, err := legacy.Migrate(ctx, database.GORM(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !report.SkippedNoActiveSuperadmin || report.LedgerEntries != 0 {
		t.Fatalf("deferred report = %+v", report)
	}
	if after := snapshotDatabase(t, database); !reflect.DeepEqual(before, after) {
		t.Fatal("deferred migration created or modified data")
	}
	putUser(t, database, firstAdmin, "superadmin", "active", 3)
	retried, err := legacy.Migrate(ctx, database.GORM(), false)
	if err != nil {
		t.Fatal(err)
	}
	if retried.SkippedNoActiveSuperadmin || retried.Profiles.Migrated != 1 || retried.Tombstones.Migrated != 1 {
		t.Fatalf("retry report = %+v", retried)
	}
}

func TestMigrateLegacyAISettingsSharedModeDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	putUser(t, database, firstAdmin, "superadmin", "active", 1)
	putJSONSetting(t, database, store.AIProfilesSettingKey, profileJSON(nil, profiles.Profile{ID: "p-shared", Name: "shared", APIKey: legacySecret, Model: "m"}), 100)
	putTombstone(t, database, "", "p-orphan", store.SyncTombstoneKindAIProfile, 200)
	before := snapshotDatabase(t, database)
	report, err := legacy.Migrate(ctx, database.GORM(), true)
	if err != nil || !report.Empty() {
		t.Fatalf("shared migration report = %+v, err = %v", report, err)
	}
	if after := snapshotDatabase(t, database); !reflect.DeepEqual(before, after) {
		t.Fatal("shared desktop/Open migration modified global or account state")
	}
	manager, err := profiles.NewManager(ctx, database, profiles.WithShared(true))
	if err != nil {
		t.Fatal(err)
	}
	overview, err := manager.Overview(ctx)
	if err != nil || len(overview.Profiles) != 1 || overview.Profiles[0].ID != "p-shared" {
		t.Fatal("shared runtime no longer reads the legacy global scope")
	}
}

func TestMigrateLegacyAISettingsPermissionConflictDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	putUser(t, database, firstAdmin, "superadmin", "active", 1)
	putJSONSetting(t, database, guard.PermissionSettingKey, guard.Config{Mode: guard.ReadOnly, DangerRules: []string{"legacy"}}, 100)
	targetKey := guard.PermissionSettingKey + ".user." + firstAdmin
	putJSONSetting(t, database, targetKey, guard.Config{Mode: guard.Silent, DangerRules: []string{"target"}}, 300)
	before, _ := readSetting(t, database, targetKey)
	report, err := legacy.Migrate(ctx, database.GORM(), false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Permissions != (legacy.ItemReport{Conflicts: 1}) {
		t.Fatalf("permission conflict report = %+v", report.Permissions)
	}
	after, _ := readSetting(t, database, targetKey)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("permission conflict changed the target value or timestamp")
	}
}

func TestMigrateLegacyAISettingsPermissionTombstoneDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	putUser(t, database, firstAdmin, "superadmin", "active", 1)
	putJSONSetting(t, database, guard.PermissionSettingKey, guard.Config{Mode: guard.ReadOnly}, 100)
	putTombstone(t, database, "", "global", "ai_permission", 200)
	report, err := legacy.Migrate(ctx, database.GORM(), false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Permissions.Tombstoned != 1 || report.Tombstones.Migrated != 1 {
		t.Fatalf("permission tombstone report = %+v", report)
	}
	if _, found := readSetting(t, database, guard.PermissionSettingKey+".user."+firstAdmin); found {
		t.Fatal("tombstoned permission was restored as active")
	}
	var row store.SyncTombstone
	if err := database.GORM().Where("owner_id = ? AND id = ? AND kind = ?", firstAdmin, "global", "ai_permission").First(&row).Error; err != nil || row.DeletedAt != 200 {
		t.Fatalf("permission tombstone was not preserved: %+v, %v", row, err)
	}
}

func TestMigrateLegacyAISettingsLedgerPreventsResurrectionDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	putUser(t, database, firstAdmin, "superadmin", "active", 1)
	putJSONSetting(t, database, store.AIProfilesSettingKey, profileJSON(nil, profiles.Profile{ID: "p-once", Name: "once", Model: "m"}), 100)
	if _, err := legacy.Migrate(ctx, database.GORM(), false); err != nil {
		t.Fatal(err)
	}
	targetKey := store.AIProfilesSettingKeyForUser(firstAdmin)
	if err := database.SettingDelete(ctx, targetKey); err != nil {
		t.Fatal(err)
	}
	before := snapshotDatabase(t, database)
	report, err := legacy.Migrate(ctx, database.GORM(), false)
	if err != nil || !report.Empty() {
		t.Fatalf("ledger reentry report = %+v, err = %v", report, err)
	}
	if _, found := readSetting(t, database, targetKey); found {
		t.Fatal("ledgered legacy profile was recreated after target deletion")
	}
	if after := snapshotDatabase(t, database); !reflect.DeepEqual(before, after) {
		t.Fatal("ledgered reentry changed database rows")
	}
}

func TestMigrateLegacyAISettingsTombstoneConflictDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	putUser(t, database, firstAdmin, "superadmin", "active", 1)
	putTombstone(t, database, "", "p-conflict", store.SyncTombstoneKindAIProfile, 100)
	putTombstone(t, database, firstAdmin, "p-conflict", store.SyncTombstoneKindAIProfile, 200)
	report, err := legacy.Migrate(ctx, database.GORM(), false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Tombstones != (legacy.ItemReport{Conflicts: 1}) {
		t.Fatalf("tombstone conflict report = %+v", report.Tombstones)
	}
	var target store.SyncTombstone
	if err := database.GORM().Where("owner_id = ? AND id = ?", firstAdmin, "p-conflict").First(&target).Error; err != nil {
		t.Fatal(err)
	}
	if target.DeletedAt != 200 {
		t.Fatalf("target tombstone timestamp = %d, want 200", target.DeletedAt)
	}
	before := snapshotDatabase(t, database)
	repeated, err := legacy.Migrate(ctx, database.GORM(), false)
	if err != nil || !repeated.Empty() {
		t.Fatalf("tombstone conflict reentry = %+v, err = %v", repeated, err)
	}
	if after := snapshotDatabase(t, database); !reflect.DeepEqual(before, after) {
		t.Fatal("tombstone conflict reentry changed database rows")
	}
}

func TestMigrateLegacyAISettingsNullTargetMapsDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	putUser(t, database, firstAdmin, "superadmin", "active", 1)
	putJSONSetting(t, database, guard.DeviceGrantsSettingKey, map[string]guard.DeviceGrant{
		"device-new": testGrant("device-new", guard.GrantKindTerminalWrite, 100),
	}, 100)
	putSetting(t, database, guard.DeviceGrantsSettingKey+".user."+firstAdmin, "null", 200)
	putJSONSetting(t, database, guard.DevicePermissionSettingKey, map[string]guard.Mode{"device-new": guard.Silent}, 100)
	putSetting(t, database, guard.DevicePermissionSettingKey+".user."+firstAdmin, "null", 200)
	report, err := legacy.Migrate(ctx, database.GORM(), false)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeviceGrants.Migrated != 1 || report.DeviceModes.Migrated != 1 {
		t.Fatalf("null target map report = %+v", report)
	}
	for _, key := range []string{
		guard.DeviceGrantsSettingKey + ".user." + firstAdmin,
		guard.DevicePermissionSettingKey + ".user." + firstAdmin,
	} {
		row, found := readSetting(t, database, key)
		if !found || row.Value == "null" || row.UpdatedAt != 200 {
			t.Fatalf("null target map was not merged safely: %s", key)
		}
	}
}
