package upgrade_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/upgrade"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func createReleasedRow(t *testing.T, database *gorm.DB, table string, row map[string]any) {
	t.Helper()
	if err := database.Table(table).Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}

func openReleasedStore(t *testing.T, schema releasedSchema) *store.Store {
	t.Helper()
	fixture := dbtest.NewFixture(t)
	var dialector gorm.Dialector
	switch fixture.Backend {
	case store.BackendSQLite:
		dialector = sqlite.Open(fixture.DSN)
	case store.BackendPostgres:
		dialector = postgres.Open(fixture.DSN)
	case store.BackendMySQL, store.BackendMariaDB:
		dialector = mysql.Open(fixture.DSN)
	default:
		t.Fatalf("unsupported fixture backend %s", fixture.Backend)
	}
	raw, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	migration := raw
	if fixture.Backend == store.BackendMySQL || fixture.Backend == store.BackendMariaDB {
		migration = migration.Set("gorm:table_options", "CHARACTER SET utf8mb4 COLLATE utf8mb4_bin")
	}
	if err := migration.AutoMigrate(schema.models()...); err != nil {
		t.Fatal(err)
	}
	seedReleasedData(t, raw)
	sqlDB, err := raw.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	return fixture.OpenStore(t)
}

func seedReleasedData(t *testing.T, database *gorm.DB) {
	t.Helper()
	putUpgradeUser(t, database, "admin-z", "superadmin", "active", 20)
	putUpgradeUser(t, database, "admin-a", "superadmin", "active", 10)
	for _, row := range []struct {
		table  string
		values map[string]any
	}{
		{"asset", map[string]any{"id": "asset", "kind": "ssh", "name": "asset", "options_json": "{}", "tags": "", "note": "", "sort": 0, "created_at": 1, "updated_at": 1, "builtin": false, "owner_id": ""}},
		{"credential", map[string]any{"id": "credential", "name": "credential", "kind": "password", "cipher": "enc:v1:released", "nonce": []byte{1, 2}, "blob": []byte{3, 4}, "kek_hint": "hint", "created_at": 2, "updated_at": 2, "owner_id": ""}},
		{"ai_conversation", map[string]any{"id": "conversation", "title": "", "scope_json": "{}", "created_at": 3, "updated_at": 3, "owner_id": ""}},
		{"ai_message", map[string]any{"id": "message", "conversation_id": "conversation", "role": "user", "content_json": "{}", "created_at": 4, "seq": 1, "owner_id": ""}},
		{"ai_run", map[string]any{"id": "run", "conversation_id": "conversation", "status": "running", "created_at": 5, "updated_at": 5, "owner_id": ""}},
		{"cron_job", map[string]any{"id": "cron", "session_id": "conversation", "name": "", "prompt": "prompt", "schedule": "* * * * *", "timezone": "UTC", "enabled": true, "timeout_ms": 1000, "model_profile_id": "", "created_at": 6, "updated_at": 6, "revision": 1, "next_run_at": 1, "owner_id": ""}},
		{"known_host", map[string]any{"id": "known-move", "host": "move.example", "port": 22, "key_type": "ssh-ed25519", "fingerprint": "move", "added_at": 7, "owner_id": ""}},
		{"known_host", map[string]any{"id": "known-legacy", "host": "conflict.example", "port": 22, "key_type": "ssh-ed25519", "fingerprint": "legacy", "added_at": 8, "owner_id": ""}},
		{"known_host", map[string]any{"id": "known-target", "host": "conflict.example", "port": 22, "key_type": "ssh-ed25519", "fingerprint": "target", "added_at": 9, "owner_id": "admin-a"}},
		{"sync_tombstone", map[string]any{"id": "sync-move", "kind": "group", "deleted_at": 10, "owner_id": ""}},
		{"sync_tombstone", map[string]any{"id": "sync-conflict", "kind": "group", "deleted_at": 11, "owner_id": ""}},
		{"sync_tombstone", map[string]any{"id": "sync-conflict", "kind": "asset", "deleted_at": 12, "owner_id": "admin-a"}},
		{"sync_tombstone", map[string]any{"id": "deleted-profile", "kind": store.SyncTombstoneKindAIProfile, "deleted_at": 13, "owner_id": ""}},
		{"credential_tombstone", map[string]any{"id": "deleted-credential", "deleted_at": 14, "owner_id": ""}},
		{"setting", map[string]any{"key": store.LayoutKey, "value": "released-layout", "updated_at": 15}},
		{"setting", map[string]any{"key": store.AIProfilesSettingKey, "value": `{"version":1,"profiles":[{"id":"profile-legacy","name":"Legacy","apiKey":"enc:v1:released-ai","model":"model"}],"activeId":"profile-legacy"}`, "updated_at": 16}},
		{"setting", map[string]any{"key": "vault.dek_envelope", "value": "released-vault", "updated_at": 17}},
		{"setting", map[string]any{"key": "sync.cursor.admin-a", "value": "released-cursor", "updated_at": 18}},
		{"user_dek", map[string]any{"user_id": "admin-a", "dek_envelope": []byte{5, 6}, "kdf_salt": []byte{7, 8}, "kdf_params": "released-kdf", "recovery_envelope": []byte{9, 10}, "recovery_hash": "recovery", "created_at": 19, "updated_at": 19}},
		{"user_totp", map[string]any{"user_id": "admin-a", "secret_envelope": []byte{11, 12}, "pending_secret_envelope": []byte{13, 14}, "created_at": 20, "updated_at": 20}},
		{"user_sync_object", map[string]any{"user_id": "admin-a", "id": "object", "seq": 21, "blob": []byte{15, 16}, "blob_hash": []byte{17, 18}}},
		{"user_sync_head", map[string]any{"user_id": "admin-a", "head_hash": "released-head"}},
		{"sync_state", map[string]any{"user_id": "admin-a", "object_id": "object", "payload_hash": []byte{19, 20}, "blob_hash": []byte{21, 22}}},
	} {
		createReleasedRow(t, database, row.table, row.values)
	}
}

func readUpgradeRow(t *testing.T, database *gorm.DB, table, where string, args ...any) map[string]any {
	t.Helper()
	rows, err := database.Table(table).Where(where, args...).Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatalf("%s row %s is missing", table, where)
	}
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := rows.Scan(destinations...); err != nil {
		t.Fatal(err)
	}
	row := make(map[string]any, len(columns))
	for index, column := range columns {
		if binary, ok := values[index].([]byte); ok {
			values[index] = append([]byte(nil), binary...)
		}
		row[column] = values[index]
	}
	return row
}

func TestMigrateReleasedGORMSchemasDatabase(t *testing.T) {
	backend := dbtest.NewFixture(t).Backend
	for _, schema := range releasedSchemas() {
		if (backend == store.BackendMySQL || backend == store.BackendMariaDB) && !schema.mysql {
			continue
		}
		t.Run(schema.name, func(t *testing.T) {
			database := openReleasedStore(t, schema)
			db := database.GORM()
			t.Logf("upgrading released GORM schema %s on %s", schema.name, database.Backend())

			beforeCredential := readUpgradeRow(t, db, "credential", "id = ?", "credential")
			beforeDEK := readUpgradeRow(t, db, "user_dek", "user_id = ?", "admin-a")
			beforeTOTP := readUpgradeRow(t, db, "user_totp", "user_id = ?", "admin-a")
			beforeObject := readUpgradeRow(t, db, "user_sync_object", "user_id = ? AND id = ?", "admin-a", "object")
			beforeHead := readUpgradeRow(t, db, "user_sync_head", "user_id = ?", "admin-a")
			beforeState := readUpgradeRow(t, db, "sync_state", "user_id = ? AND object_id = ?", "admin-a", "object")
			beforeKnownLegacy := readUpgradeRow(t, db, "known_host", "id = ?", "known-legacy")
			beforeKnownTarget := readUpgradeRow(t, db, "known_host", "id = ?", "known-target")
			beforeSyncLegacy := readUpgradeRow(t, db, "sync_tombstone", "id = ? AND owner_id = ''", "sync-conflict")
			beforeSyncTarget := readUpgradeRow(t, db, "sync_tombstone", "id = ? AND owner_id = ?", "sync-conflict", "admin-a")

			report, err := upgrade.Migrate(context.Background(), db, false)
			if err != nil {
				t.Fatal(err)
			}
			if report.OwnerID != "admin-a" || report.DeferredNoActiveSuperadmin || report.Ownership.Assigned != 6 || report.Ownership.Conflicts != 2 {
				t.Fatalf("released ownership report = %+v", report)
			}
			// Released v1 cron rows predate owner kinds; they stay ownerless
			// (the scheduler disables them) and are reported as unattributed.
			if report.Layout.Copied != 1 || report.AI.CronJobsAssigned != 0 || report.AI.RunsAssigned != 1 || report.AI.MessagesAssigned != 1 || report.AI.Unattributed != 1 {
				t.Fatalf("released layout/AI report = %+v", report)
			}
			if report.AISettings.Profiles.Migrated != 1 || report.AISettings.Tombstones.Migrated != 1 {
				t.Fatalf("released AI settings report = %+v", report.AISettings)
			}
			for _, check := range []struct {
				model    any
				idColumn string
				id       string
			}{
				{&store.AssetRow{}, "id", "asset"},
				{&store.CredentialRow{}, "id", "credential"},
				{&store.ConversationRow{}, "id", "conversation"},
				{&store.MessageRow{}, "id", "message"},
				{&store.RunRow{}, "id", "run"},
				{&store.KnownHostRow{}, "id", "known-move"},
				{&store.SyncTombstone{}, "id", "sync-move"},
				{&store.CredentialTombstone{}, "id", "deleted-credential"},
			} {
				requireUpgradeOwner(t, db, check.model, check.idColumn, check.id, "admin-a")
			}
			requireUpgradeOwner(t, db, &store.KnownHostRow{}, "id", "known-legacy", "")
			requireUpgradeOwner(t, db, &store.CronJob{}, "id", "cron", "")
			requireUpgradeSetting(t, db, store.LayoutKey, "released-layout", 15)
			requireUpgradeSetting(t, db, store.LayoutSettingKeyForUser("admin-a"), "released-layout", 15)
			requireUpgradeSetting(t, db, "vault.dek_envelope", "released-vault", 17)
			requireUpgradeSetting(t, db, "sync.cursor.admin-a", "released-cursor", 18)
			requireUpgradeSetting(t, db, store.AIProfilesSettingKey, `{"version":1,"profiles":[{"id":"profile-legacy","name":"Legacy","apiKey":"enc:v1:released-ai","model":"model"}],"activeId":"profile-legacy"}`, 16)

			if _, binaryText := beforeCredential["owner_id"].([]byte); binaryText {
				beforeCredential["owner_id"] = []byte("admin-a")
			} else {
				beforeCredential["owner_id"] = "admin-a"
			}
			for _, check := range []struct {
				name string
				got  map[string]any
				want map[string]any
			}{
				{"credential", readUpgradeRow(t, db, "credential", "id = ?", "credential"), beforeCredential},
				{"DEK", readUpgradeRow(t, db, "user_dek", "user_id = ?", "admin-a"), beforeDEK},
				{"TOTP", readUpgradeRow(t, db, "user_totp", "user_id = ?", "admin-a"), beforeTOTP},
				{"sync object", readUpgradeRow(t, db, "user_sync_object", "user_id = ? AND id = ?", "admin-a", "object"), beforeObject},
				{"sync head", readUpgradeRow(t, db, "user_sync_head", "user_id = ?", "admin-a"), beforeHead},
				{"sync state", readUpgradeRow(t, db, "sync_state", "user_id = ? AND object_id = ?", "admin-a", "object"), beforeState},
				{"known host conflict source", readUpgradeRow(t, db, "known_host", "id = ?", "known-legacy"), beforeKnownLegacy},
				{"known host conflict target", readUpgradeRow(t, db, "known_host", "id = ?", "known-target"), beforeKnownTarget},
				{"sync conflict source", readUpgradeRow(t, db, "sync_tombstone", "id = ? AND owner_id = ''", "sync-conflict"), beforeSyncLegacy},
				{"sync conflict target", readUpgradeRow(t, db, "sync_tombstone", "id = ? AND owner_id = ?", "sync-conflict", "admin-a"), beforeSyncTarget},
			} {
				if !reflect.DeepEqual(check.got, check.want) {
					t.Fatalf("%s changed:\ngot  %+v\nwant %+v", check.name, check.got, check.want)
				}
			}

			var targetProfiles store.Setting
			if err := db.Where(upgradeSettingKeyEquals(store.AIProfilesSettingKeyForUser("admin-a"))).First(&targetProfiles).Error; err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Profiles []struct {
					ID     string `json:"id"`
					APIKey string `json:"apiKey"`
				} `json:"profiles"`
			}
			if err := json.Unmarshal([]byte(targetProfiles.Value), &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded.Profiles) != 1 || decoded.Profiles[0].ID != "profile-legacy" || decoded.Profiles[0].APIKey != "enc:v1:released-ai" {
				t.Fatalf("migrated AI profiles = %+v", decoded.Profiles)
			}
			var globalAITombstone store.SyncTombstone
			if err := db.Where("id = ? AND owner_id = ''", "deleted-profile").First(&globalAITombstone).Error; err != nil {
				t.Fatal(err)
			}
			if globalAITombstone.Kind != store.SyncTombstoneKindAIProfile || globalAITombstone.DeletedAt != 13 {
				t.Fatalf("global AI tombstone = %+v", globalAITombstone)
			}
			var aiTombstone store.SyncTombstone
			if err := db.Where("id = ? AND owner_id = ?", "deleted-profile", "admin-a").First(&aiTombstone).Error; err != nil {
				t.Fatal(err)
			}
			if aiTombstone.Kind != store.SyncTombstoneKindAIProfile || aiTombstone.DeletedAt != 13 {
				t.Fatalf("AI tombstone changed by generic sync migration: %+v", aiTombstone)
			}
			afterFirst := snapshotUpgradeFault(t, db)

			repeated, err := upgrade.Migrate(context.Background(), db, false)
			if err != nil {
				t.Fatal(err)
			}
			if repeated.Ownership.Assigned != 0 || repeated.Ownership.Conflicts != 2 || repeated.Layout.Existing != 1 || repeated.AI.Changed() || !repeated.AISettings.Empty() {
				t.Fatalf("released idempotent reentry report = %+v", repeated)
			}
			if afterSecond := snapshotUpgradeFault(t, db); !reflect.DeepEqual(afterSecond, afterFirst) {
				t.Fatalf("reentry rewrote upgraded rows:\nfirst  %+v\nsecond %+v", afterFirst, afterSecond)
			}
		})
	}
}
