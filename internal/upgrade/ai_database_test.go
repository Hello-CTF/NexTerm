package upgrade_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/upgrade"
	"gorm.io/gorm"
)

func openCronJob(id, sessionID string) *store.CronJob {
	return &store.CronJob{
		ID: id, SessionID: sessionID, OwnerKind: string(cron.OwnerKindOpen), Prompt: "prompt", Schedule: "* * * * *",
		Timezone: "UTC", Enabled: true, TimeoutMS: 1000, CreatedAt: 1, UpdatedAt: 1, Revision: 1, NextRunAt: 1,
	}
}

func loadUpgradeCron(t *testing.T, database *gorm.DB, id string) store.CronJob {
	t.Helper()
	var job store.CronJob
	if err := database.Where("id = ?", id).First(&job).Error; err != nil {
		t.Fatal(err)
	}
	return job
}

func TestMigrateOpenModeAIHistoryToAccountDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	db := database.GORM()
	putUpgradeUser(t, db, "admin", "superadmin", "active", 1)
	v1Cron := openCronJob("cron-v1", "conversation")
	v1Cron.OwnerKind = ""
	for _, row := range []any{
		&store.ConversationRow{ID: "conversation", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1},
		&store.ConversationRow{ID: "deleted-conversation", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1},
		&store.SyncTombstone{ID: "deleted-conversation", Kind: "conversation", DeletedAt: 2},
		&store.MessageRow{ID: "message", ConversationID: "conversation", Role: "user", ContentJSON: "{}", CreatedAt: 1, Seq: 1},
		&store.MessageRow{ID: "orphan-message", ConversationID: "deleted-conversation", Role: "user", ContentJSON: "{}", CreatedAt: 1, Seq: 1},
		&store.RunRow{ID: "run", ConversationID: "conversation", Status: "done", CreatedAt: 1, UpdatedAt: 1},
		&store.RunEventRow{RunID: "run", Seq: 1, Type: "start", PayloadJSON: "{}", CreatedAt: 1},
		&store.RunEventRow{RunID: "run", Seq: 2, Type: "end", PayloadJSON: "{}", CreatedAt: 2},
		&store.HitlRunRow{ID: "run", Data: []byte("{}"), UpdatedAt: 1},
		&store.CheckpointRow{ID: "run", Data: []byte("{}"), UpdatedAt: 1},
		openCronJob("cron-open", "conversation"),
		openCronJob("cron-lost", "missing"),
		openCronJob("cron-deleted", "deleted-conversation"),
		v1Cron,
	} {
		createUpgradeRow(t, db, row)
	}

	report, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	want := upgrade.AIHistoryReport{
		CronJobsAssigned: 1, CronJobsDisabled: 2, RunsAssigned: 1, MessagesAssigned: 1,
		RunEventsAssigned: 2, HitlRunsAssigned: 1, CheckpointsAssigned: 1, Unattributed: 4,
	}
	if report.OwnerID != "admin" || report.AI != want {
		t.Fatalf("AI history report = %+v, want %+v", report.AI, want)
	}
	for _, check := range []struct {
		model    any
		idColumn string
		id       string
	}{
		{&store.ConversationRow{}, "id", "conversation"},
		{&store.MessageRow{}, "id", "message"},
		{&store.RunRow{}, "id", "run"},
		{&store.HitlRunRow{}, "id", "run"},
		{&store.CheckpointRow{}, "id", "run"},
		{&store.CronJob{}, "id", "cron-open"},
	} {
		requireUpgradeOwner(t, db, check.model, check.idColumn, check.id, "admin")
	}
	var eventOwners []string
	if err := db.Model(&store.RunEventRow{}).Where("run_id = ?", "run").Distinct("owner_id").Pluck("owner_id", &eventOwners).Error; err != nil {
		t.Fatal(err)
	}
	if len(eventOwners) != 1 || eventOwners[0] != "admin" {
		t.Fatalf("run event owners = %v, want admin", eventOwners)
	}
	requireUpgradeOwner(t, db, &store.MessageRow{}, "id", "orphan-message", "")

	for _, id := range []string{"cron-lost", "cron-deleted"} {
		job := loadUpgradeCron(t, db, id)
		if job.OwnerID != "" || job.Enabled || job.LastError != cron.ErrOpenJobUnattributed.Error() {
			t.Fatalf("unattributable Open cron %s = %+v, want disabled with a visible error", id, job)
		}
	}
	if job := loadUpgradeCron(t, db, "cron-v1"); job.OwnerID != "" || job.OwnerKind != "" || !job.Enabled || job.LastError != "" {
		t.Fatalf("v1 cron without owner kind was changed by the upgrade: %+v", job)
	}

	cronStore, err := cron.NewSQLStore(context.Background(), db, cron.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := cronStore.List(ipc.WithUserID(context.Background(), "admin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != "cron-open" || jobs[0].OwnerID != "admin" || jobs[0].OwnerKind != cron.OwnerKindAccount || !jobs[0].Enabled {
		t.Fatalf("admin cron jobs after upgrade = %+v", jobs)
	}

	before := snapshotUpgradeFault(t, db)
	repeated, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.AI != (upgrade.AIHistoryReport{}) {
		t.Fatalf("idempotent AI history reentry = %+v", repeated.AI)
	}
	if after := snapshotUpgradeFault(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("idempotent AI history reentry changed rows:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestMigrateOpenModeAISettingsToAccountDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	db := database.GORM()
	putUpgradeUser(t, db, "admin", "superadmin", "active", 1)
	profilesValue := `{"version":1,"profiles":[{"id":"p-a","name":"A","apiKey":"enc:v1:a","model":"m"},{"id":"p-b","name":"B","apiKey":"enc:v1:b","model":"m"},{"id":"p-gone","name":"Gone","apiKey":"enc:v1:c","model":"m"}],"activeId":"p-a"}`
	grants := `{"device-1":{"deviceId":"device-1","kinds":["exec"],"permanent":true,"updatedAt":3}}`
	rules := `[{"id":"rule-1","deviceId":"device-1","action":"read","path":"/etc","createdAt":4,"updatedAt":4}]`
	modes := `{"device-1":"read_only"}`
	permission := `{"mode":"silent","dangerRules":["rm -rf"]}`
	for _, row := range []any{
		&store.Setting{Key: store.AIProfilesSettingKey, Value: profilesValue, UpdatedAt: 10},
		&store.Setting{Key: guard.PermissionSettingKey, Value: permission, UpdatedAt: 11},
		&store.Setting{Key: guard.DeviceGrantsSettingKey, Value: grants, UpdatedAt: 12},
		&store.Setting{Key: guard.GrantRulesSettingKey, Value: rules, UpdatedAt: 13},
		&store.Setting{Key: guard.DevicePermissionSettingKey, Value: modes, UpdatedAt: 14},
		&store.SyncTombstone{ID: "p-gone", Kind: store.SyncTombstoneKindAIProfile, DeletedAt: 15},
	} {
		createUpgradeRow(t, db, row)
	}

	report, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	settings := report.AISettings
	if settings.Profiles.Migrated != 2 || settings.Profiles.Tombstoned != 1 || settings.Permissions.Migrated != 1 ||
		settings.DeviceGrants.Migrated != 1 || settings.GrantRules.Migrated != 1 || settings.DeviceModes.Migrated != 1 ||
		settings.Tombstones.Migrated != 1 || settings.Total().Conflicts != 0 {
		t.Fatalf("AI settings report = %+v", settings)
	}
	profileIDs := readUpgradeProfileIDs(t, db, "admin")
	if len(profileIDs) != 2 || profileIDs[0] != "p-a" || profileIDs[1] != "p-b" {
		t.Fatalf("copied AI profiles = %v", profileIDs)
	}
	requireUpgradeSetting(t, db, guard.PermissionSettingKey+".user.admin", permission, 11)
	requireUpgradeSetting(t, db, guard.DevicePermissionSettingKey+".user.admin", modes, 14)
	requireUpgradeSetting(t, db, store.AIProfilesSettingKey, profilesValue, 10)
	var grantRows map[string]guard.DeviceGrant
	readUpgradeJSONSetting(t, db, guard.DeviceGrantsSettingKey+".user.admin", &grantRows)
	if grant, ok := grantRows["device-1"]; !ok || !grant.Permanent {
		t.Fatalf("copied device grants = %+v", grantRows)
	}
	var ruleRows []guard.GrantRule
	readUpgradeJSONSetting(t, db, guard.GrantRulesSettingKey+".user.admin", &ruleRows)
	if len(ruleRows) != 1 || ruleRows[0].ID != "rule-1" {
		t.Fatalf("copied grant rules = %+v", ruleRows)
	}
	var copiedTombstone store.SyncTombstone
	if err := db.Where("id = ? AND owner_id = ?", "p-gone", "admin").First(&copiedTombstone).Error; err != nil {
		t.Fatal(err)
	}

	// The admin deletes a copied profile without a sync tombstone (sync opt-in
	// off). Restarting must not restore it while the Open-mode settings are
	// unchanged.
	if err := db.Model(&store.Setting{}).Where(upgradeSettingKeyEquals(store.AIProfilesSettingKeyForUser("admin"))).
		UpdateColumn("value", `{"version":1,"profiles":[{"id":"p-a","name":"A","apiKey":"enc:v1:a","model":"m"}],"activeId":"p-a"}`).Error; err != nil {
		t.Fatal(err)
	}
	before := snapshotUpgradeFault(t, db)
	repeated, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.AISettings.Empty() {
		t.Fatalf("unchanged Open-mode settings were merged again: %+v", repeated.AISettings)
	}
	if after := snapshotUpgradeFault(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("unchanged Open-mode settings rewrote rows:\nbefore %+v\nafter  %+v", before, after)
	}

	// Another Open-mode session adds a profile; the next account start copies it.
	if err := db.Model(&store.Setting{}).Where(upgradeSettingKeyEquals(store.AIProfilesSettingKey)).
		UpdateColumn("value", `{"version":1,"profiles":[{"id":"p-a","name":"A","apiKey":"enc:v1:a","model":"m"},{"id":"p-new","name":"New","apiKey":"enc:v1:n","model":"m"}],"activeId":"p-a"}`).Error; err != nil {
		t.Fatal(err)
	}
	changed, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if changed.AISettings.Profiles.Migrated != 1 || changed.AISettings.Profiles.Existing != 1 || changed.AISettings.Total().Conflicts != 0 {
		t.Fatalf("changed Open-mode settings report = %+v", changed.AISettings)
	}
	profileIDs = readUpgradeProfileIDs(t, db, "admin")
	if len(profileIDs) != 2 || profileIDs[0] != "p-a" || profileIDs[1] != "p-new" {
		t.Fatalf("profiles after Open-mode change = %v", profileIDs)
	}
}

func TestMigrateOpenModeAISettingsConflictsDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	db := database.GORM()
	putUpgradeUser(t, db, "admin", "superadmin", "active", 1)
	targetProfiles := `{"version":1,"profiles":[{"id":"p-a","name":"Target","apiKey":"enc:v1:t","model":"m"}],"activeId":"p-a"}`
	targetPermission := `{"mode":"read_only","dangerRules":[]}`
	for _, row := range []any{
		&store.Setting{Key: store.AIProfilesSettingKey, Value: `{"version":1,"profiles":[{"id":"p-a","name":"Open","apiKey":"enc:v1:o","model":"m"}],"activeId":"p-a"}`, UpdatedAt: 10},
		&store.Setting{Key: store.AIProfilesSettingKeyForUser("admin"), Value: targetProfiles, UpdatedAt: 20},
		&store.Setting{Key: guard.PermissionSettingKey, Value: `{"mode":"silent","dangerRules":[]}`, UpdatedAt: 11},
		&store.Setting{Key: guard.PermissionSettingKey + ".user.admin", Value: targetPermission, UpdatedAt: 21},
	} {
		createUpgradeRow(t, db, row)
	}
	report, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.AISettings.Profiles.Conflicts != 1 || report.AISettings.Permissions.Conflicts != 1 || report.AISettings.Total().Migrated != 0 {
		t.Fatalf("conflict report = %+v", report.AISettings)
	}
	requireUpgradeSetting(t, db, store.AIProfilesSettingKeyForUser("admin"), targetProfiles, 20)
	requireUpgradeSetting(t, db, guard.PermissionSettingKey+".user.admin", targetPermission, 21)
	repeated, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.AISettings.Empty() {
		t.Fatalf("reported conflicts again for unchanged Open-mode settings: %+v", repeated.AISettings)
	}
}

func readUpgradeJSONSetting(t *testing.T, database *gorm.DB, key string, dest any) {
	t.Helper()
	var row store.Setting
	if err := database.Where(upgradeSettingKeyEquals(key)).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(row.Value), dest); err != nil {
		t.Fatal(err)
	}
}

func readUpgradeProfileIDs(t *testing.T, database *gorm.DB, ownerID string) []string {
	t.Helper()
	var decoded struct {
		Profiles []struct {
			ID string `json:"id"`
		} `json:"profiles"`
	}
	readUpgradeJSONSetting(t, database, store.AIProfilesSettingKeyForUser(ownerID), &decoded)
	ids := make([]string, 0, len(decoded.Profiles))
	for _, profile := range decoded.Profiles {
		ids = append(ids, profile.ID)
	}
	return ids
}
