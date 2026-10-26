package upgrade_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/upgrade"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func upgradeSettingKeyEquals(key string) clause.Expression {
	return clause.Eq{Column: clause.Column{Name: "key"}, Value: key}
}

func upgradeSettingKeysIn(keys ...string) clause.Expression {
	values := make([]any, len(keys))
	for index, key := range keys {
		values[index] = key
	}
	return clause.IN{Column: clause.Column{Name: "key"}, Values: values}
}

func upgradeSettingOrder() clause.OrderBy {
	return clause.OrderBy{Columns: []clause.OrderByColumn{{Column: clause.Column{Name: "key"}}}}
}

func createUpgradeRow(t *testing.T, database *gorm.DB, row any) {
	t.Helper()
	if err := database.Create(row).Error; err != nil {
		t.Fatal(err)
	}
}

func putUpgradeUser(t *testing.T, database *gorm.DB, id, role, state string, createdAt int64) {
	t.Helper()
	createUpgradeRow(t, database, &store.AppUser{
		ID: id, Username: id + "-name", Role: role, PasswordHash: "hash-" + id,
		State: state, CreatedAt: createdAt, UpdatedAt: createdAt,
	})
}

func requireUpgradeOwner(t *testing.T, database *gorm.DB, model any, idColumn, id, want string) {
	t.Helper()
	var ownerID string
	result := database.Model(model).Select("owner_id").Where(idColumn+" = ?", id).Scan(&ownerID)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if result.RowsAffected != 1 || ownerID != want {
		t.Fatalf("%T %s=%s owner = %q (rows %d), want %q", model, idColumn, id, ownerID, result.RowsAffected, want)
	}
}

func requireUpgradeSetting(t *testing.T, database *gorm.DB, key, wantValue string, wantUpdatedAt int64) {
	t.Helper()
	var row store.Setting
	if err := database.Where(upgradeSettingKeyEquals(key)).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Value != wantValue || row.UpdatedAt != wantUpdatedAt {
		t.Fatalf("setting %s = %+v, want value %q updatedAt %d", key, row, wantValue, wantUpdatedAt)
	}
}

func TestMigrateOwnershipAndOpaqueDataDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	db := database.GORM()
	putUpgradeUser(t, db, "admin-disabled", "superadmin", "disabled", 1)
	putUpgradeUser(t, db, "ordinary", "user", "active", 2)
	putUpgradeUser(t, db, "admin-z", "superadmin", "active", 20)
	putUpgradeUser(t, db, "admin-a", "superadmin", "active", 10)

	rows := []any{
		&store.AssetGroupRow{ID: "group", Name: "group", CreatedAt: 1, UpdatedAt: 1},
		&store.AssetRow{ID: "asset", Kind: "ssh", Name: "asset", OptionsJSON: "{}", CreatedAt: 2, UpdatedAt: 2},
		&store.AssetRow{ID: "owned-asset", Kind: "ssh", Name: "owned", OptionsJSON: "{}", CreatedAt: 3, UpdatedAt: 3, OwnerID: "admin-z"},
		&store.SnippetRow{ID: "snippet", Name: "snippet", Body: "echo ok", CreatedAt: 4, UpdatedAt: 4},
		&store.ConversationRow{ID: "conversation", ScopeJSON: "{}", CreatedAt: 5, UpdatedAt: 5},
		&store.MessageRow{ID: "message", ConversationID: "conversation", Role: "user", ContentJSON: "{}", CreatedAt: 6, Seq: 1},
		&store.RunRow{ID: "run", ConversationID: "conversation", Status: "running", CreatedAt: 7, UpdatedAt: 7},
		&store.CronJob{ID: "cron", SessionID: "conversation", Prompt: "prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: true, TimeoutMS: 1000, CreatedAt: 8, UpdatedAt: 8, Revision: 1, NextRunAt: 1},
		&store.TranscriptRow{ID: "transcript", SessionID: "session", AssetID: "asset", StartedAt: 9},
		&store.TranscriptChunkRow{TranscriptID: "transcript", Seq: 1, TS: 10, Data: []byte{0, 1, 2}},
		&store.DurableTranscriptOffset{DurableID: "durable", Offset: 11, UpdatedAt: 11},
		&store.DurableTabTitle{DurableID: "durable", Title: "title", UpdatedAt: 12},
		&store.RecordingRow{ID: "recording", SessionID: "session", TabID: "tab", Path: "/tmp/recording", StartedAt: 13},
		&store.AuditRow{ID: 1, TS: 14, Source: "test", Kind: "upgrade", PayloadJSON: "{}"},
		&store.CredentialRow{ID: "credential", Name: "credential", Kind: "password", Cipher: "enc:v1:opaque", Nonce: []byte{3, 2, 1}, Blob: []byte{9, 8, 7}, KEKHint: "hint", CreatedAt: 15, UpdatedAt: 15},
		&store.KnownHostRow{ID: "known", Host: "host.example", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "fingerprint", AddedAt: 16},
		&store.OutcomeRecord{IdempotenceKey: "outcome", AuthorizationID: "authorization", Kind: "test", CanonicalArgs: "{}", State: "done", Outcome: "ok", AuditState: "done", Revision: 1, CreatedAt: 17},
		&store.SyncTombstone{ID: "deleted-group", Kind: "group", DeletedAt: 18},
		&store.CredentialTombstone{ID: "deleted-credential", DeletedAt: 19},
		&store.Setting{Key: store.LayoutKey, Value: "opaque-layout", UpdatedAt: 20},
		&store.Setting{Key: "vault.dek_envelope", Value: "vault-envelope", UpdatedAt: 21},
		&store.Setting{Key: "vault.master_salt", Value: "vault-salt", UpdatedAt: 22},
		&store.Setting{Key: "sync.cursor.admin-a", Value: "opaque-cursor", UpdatedAt: 23},
		&store.UserDEK{UserID: "admin-a", DEKEnvelope: []byte{1, 3, 5}, KDFSalt: []byte{2, 4, 6}, KDFParams: "opaque-kdf", RecoveryEnvelope: []byte{7, 9}, RecoveryHash: "recovery-hash", CreatedAt: 24, UpdatedAt: 24},
		&store.UserTOTP{UserID: "admin-a", SecretEnvelope: []byte{8, 6, 4}, PendingSecretEnvelope: []byte{2, 3}, CreatedAt: 25, UpdatedAt: 25},
		&store.UserSyncObject{UserID: "admin-a", ID: "object", Seq: 26, Blob: []byte{0, 255, 1}, BlobHash: []byte{4, 5, 6}},
		&store.UserSyncHead{UserID: "admin-a", HeadHash: "opaque-head"},
		&store.SyncState{UserID: "admin-a", ObjectID: "object", PayloadHash: []byte{7, 8}, BlobHash: []byte{9, 10}},
	}
	for _, row := range rows {
		createUpgradeRow(t, db, row)
	}

	var beforeCredential store.CredentialRow
	var beforeDEK store.UserDEK
	var beforeTOTP store.UserTOTP
	var beforeObject store.UserSyncObject
	var beforeHead store.UserSyncHead
	var beforeSyncState store.SyncState
	if err := db.Where("id = ?", "credential").First(&beforeCredential).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("user_id = ?", "admin-a").First(&beforeDEK).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("user_id = ?", "admin-a").First(&beforeTOTP).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("user_id = ? AND id = ?", "admin-a", "object").First(&beforeObject).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("user_id = ?", "admin-a").First(&beforeHead).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("user_id = ? AND object_id = ?", "admin-a", "object").First(&beforeSyncState).Error; err != nil {
		t.Fatal(err)
	}

	report, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.OwnerID != "admin-a" || report.DeferredNoActiveSuperadmin {
		t.Fatalf("owner selection/deferral = %+v", report)
	}
	if report.Ownership.Assigned != 15 || report.Ownership.Conflicts != 0 {
		t.Fatalf("ownership report = %+v, want 15 assignments and no conflicts", report.Ownership)
	}
	if report.Layout.Copied != 1 {
		t.Fatalf("layout/AI report = %+v", report)
	}
	for _, check := range []struct {
		model    any
		idColumn string
		id       string
	}{
		{&store.AssetGroupRow{}, "id", "group"},
		{&store.AssetRow{}, "id", "asset"},
		{&store.SnippetRow{}, "id", "snippet"},
		{&store.ConversationRow{}, "id", "conversation"},
		{&store.TranscriptRow{}, "id", "transcript"},
		{&store.TranscriptChunkRow{}, "transcript_id", "transcript"},
		{&store.DurableTranscriptOffset{}, "durable_id", "durable"},
		{&store.DurableTabTitle{}, "durable_id", "durable"},
		{&store.RecordingRow{}, "id", "recording"},
		{&store.AuditRow{}, "id", "1"},
		{&store.CredentialRow{}, "id", "credential"},
		{&store.KnownHostRow{}, "id", "known"},
		{&store.OutcomeRecord{}, "idempotence_key", "outcome"},
		{&store.SyncTombstone{}, "id", "deleted-group"},
		{&store.CredentialTombstone{}, "id", "deleted-credential"},
	} {
		requireUpgradeOwner(t, db, check.model, check.idColumn, check.id, "admin-a")
	}
	requireUpgradeOwner(t, db, &store.AssetRow{}, "id", "owned-asset", "admin-z")
	requireUpgradeSetting(t, db, store.LayoutKey, "opaque-layout", 20)
	requireUpgradeSetting(t, db, store.LayoutSettingKeyForUser("admin-a"), "opaque-layout", 20)
	requireUpgradeSetting(t, db, "vault.dek_envelope", "vault-envelope", 21)
	requireUpgradeSetting(t, db, "vault.master_salt", "vault-salt", 22)
	requireUpgradeSetting(t, db, "sync.cursor.admin-a", "opaque-cursor", 23)

	beforeCredential.OwnerID = "admin-a"
	var afterCredential store.CredentialRow
	var afterDEK store.UserDEK
	var afterTOTP store.UserTOTP
	var afterObject store.UserSyncObject
	var afterHead store.UserSyncHead
	var afterSyncState store.SyncState
	if err := db.Where("id = ?", "credential").First(&afterCredential).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("user_id = ?", "admin-a").First(&afterDEK).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("user_id = ?", "admin-a").First(&afterTOTP).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("user_id = ? AND id = ?", "admin-a", "object").First(&afterObject).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("user_id = ?", "admin-a").First(&afterHead).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("user_id = ? AND object_id = ?", "admin-a", "object").First(&afterSyncState).Error; err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name      string
		got, want any
	}{
		{"credential", afterCredential, beforeCredential},
		{"user DEK", afterDEK, beforeDEK},
		{"TOTP", afterTOTP, beforeTOTP},
		{"sync object", afterObject, beforeObject},
		{"sync head", afterHead, beforeHead},
		{"sync state", afterSyncState, beforeSyncState},
	} {
		if !reflect.DeepEqual(check.got, check.want) {
			t.Fatalf("%s changed beyond allowed ownership: got %+v want %+v", check.name, check.got, check.want)
		}
	}

	repeated, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Ownership.Assigned != 0 || repeated.Layout.Copied != 0 || repeated.Layout.Existing != 1 {
		t.Fatalf("idempotent reentry report = %+v", repeated)
	}
}

type conflictSnapshot struct {
	knownHosts  []store.KnownHostRow
	outcomes    []store.OutcomeRecord
	tombstones  []store.SyncTombstone
	credentials []store.CredentialTombstone
	settings    []store.Setting
}

func snapshotConflicts(t *testing.T, database *gorm.DB) conflictSnapshot {
	t.Helper()
	var snapshot conflictSnapshot
	for _, query := range []struct {
		dest  any
		order string
	}{
		{&snapshot.knownHosts, "id"},
		{&snapshot.outcomes, "idempotence_key"},
		{&snapshot.tombstones, "id"},
		{&snapshot.credentials, "id"},
		{&snapshot.settings, "key"},
	} {
		statement := database.Order(query.order)
		if query.order == "key" {
			statement = database.Clauses(upgradeSettingOrder())
		}
		if err := statement.Find(query.dest).Error; err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func TestMigratePreservesConflictsDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	db := database.GORM()
	putUpgradeUser(t, db, "admin", "superadmin", "active", 1)
	for _, row := range []any{
		&store.KnownHostRow{ID: "known-legacy", Host: "conflict.example", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "legacy", AddedAt: 10},
		&store.KnownHostRow{ID: "known-target", Host: "conflict.example", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "target", AddedAt: 20, OwnerID: "admin"},
		&store.OutcomeRecord{IdempotenceKey: "outcome-conflict", AuthorizationID: "legacy", Kind: "test", CanonicalArgs: "{}", State: "done", Outcome: "legacy", AuditState: "done", Revision: 1, CreatedAt: 10},
		&store.OutcomeRecord{IdempotenceKey: "outcome-conflict", AuthorizationID: "target", Kind: "test", CanonicalArgs: "{}", State: "done", Outcome: "target", AuditState: "done", Revision: 2, CreatedAt: 20, OwnerID: "admin"},
		&store.SyncTombstone{ID: "sync-conflict", Kind: "group", DeletedAt: 10},
		&store.SyncTombstone{ID: "sync-conflict", Kind: "asset", DeletedAt: 20, OwnerID: "admin"},
		&store.CredentialTombstone{ID: "credential-conflict", DeletedAt: 10},
		&store.CredentialTombstone{ID: "credential-conflict", DeletedAt: 20, OwnerID: "admin"},
		&store.Setting{Key: store.LayoutKey, Value: "legacy-layout", UpdatedAt: 10},
		&store.Setting{Key: store.LayoutSettingKeyForUser("admin"), Value: "target-layout", UpdatedAt: 20},
	} {
		createUpgradeRow(t, db, row)
	}
	before := snapshotConflicts(t, db)
	for attempt := 0; attempt < 2; attempt++ {
		report, err := upgrade.Migrate(context.Background(), db, false)
		if err != nil {
			t.Fatal(err)
		}
		if report.Ownership.Assigned != 0 || report.Ownership.Conflicts != 4 || report.Layout.Conflicts != 1 {
			t.Fatalf("attempt %d conflict report = %+v", attempt+1, report)
		}
		after := snapshotConflicts(t, db)
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("attempt %d changed conflict rows:\nbefore %+v\nafter  %+v", attempt+1, before, after)
		}
	}
	requireUpgradeOwner(t, db, &store.KnownHostRow{}, "id", "known-legacy", "")
}

func TestMigrateDefersWithoutActiveSuperadminDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	db := database.GORM()
	putUpgradeUser(t, db, "disabled", "superadmin", "disabled", 1)
	putUpgradeUser(t, db, "reset", "superadmin", "reset_required", 2)
	putUpgradeUser(t, db, "ordinary", "user", "active", 3)
	for _, row := range []any{
		&store.AssetRow{ID: "asset", Kind: "ssh", Name: "asset", OptionsJSON: "{}", CreatedAt: 4, UpdatedAt: 4},
		&store.ConversationRow{ID: "conversation", ScopeJSON: "{}", CreatedAt: 5, UpdatedAt: 5},
		&store.CronJob{ID: "cron", SessionID: "conversation", Prompt: "prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: true, TimeoutMS: 1000, CreatedAt: 6, UpdatedAt: 6, Revision: 1, NextRunAt: 1},
		&store.Setting{Key: store.LayoutKey, Value: "layout", UpdatedAt: 7},
	} {
		createUpgradeRow(t, db, row)
	}

	deferred, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if !deferred.DeferredNoActiveSuperadmin || deferred.OwnerID != "" || deferred.Ownership != (upgrade.OwnershipReport{}) {
		t.Fatalf("deferred report = %+v", deferred)
	}
	requireUpgradeOwner(t, db, &store.AssetRow{}, "id", "asset", "")
	requireUpgradeOwner(t, db, &store.ConversationRow{}, "id", "conversation", "")
	requireUpgradeOwner(t, db, &store.CronJob{}, "id", "cron", "")
	var cron store.CronJob
	if err := db.Where("id = ?", "cron").First(&cron).Error; err != nil {
		t.Fatal(err)
	}
	if !cron.Enabled || cron.OwnerKind != "" || cron.LastError != "" {
		t.Fatalf("deferred cron was quarantined: %+v", cron)
	}

	putUpgradeUser(t, db, "admin-z", "superadmin", "active", 20)
	putUpgradeUser(t, db, "admin-a", "superadmin", "active", 10)
	retried, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if retried.DeferredNoActiveSuperadmin || retried.OwnerID != "admin-a" {
		t.Fatalf("retry report = %+v", retried)
	}
	requireUpgradeOwner(t, db, &store.AssetRow{}, "id", "asset", "admin-a")
	requireUpgradeSetting(t, db, store.LayoutSettingKeyForUser("admin-a"), "layout", 7)
}

func TestMigrateSharedKeepsOwnerlessDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	db := database.GORM()
	putUpgradeUser(t, db, "admin", "superadmin", "active", 1)
	for _, row := range []any{
		&store.AssetRow{ID: "asset", Kind: "ssh", Name: "asset", OptionsJSON: "{}", CreatedAt: 2, UpdatedAt: 2},
		&store.ConversationRow{ID: "conversation", ScopeJSON: "{}", CreatedAt: 3, UpdatedAt: 3},
		&store.CronJob{ID: "cron", SessionID: "conversation", Prompt: "prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: true, TimeoutMS: 1000, CreatedAt: 4, UpdatedAt: 4, Revision: 1, NextRunAt: 1},
		&store.SyncTombstone{ID: "group", Kind: "group", DeletedAt: 5},
		&store.Setting{Key: store.LayoutKey, Value: "layout", UpdatedAt: 6},
		&store.Setting{Key: store.AIProfilesSettingKey, Value: `{"version":1,"profiles":[],"activeId":null}`, UpdatedAt: 7},
	} {
		createUpgradeRow(t, db, row)
	}
	before := snapshotConflicts(t, db)
	report, err := upgrade.Migrate(context.Background(), db, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.OwnerID != "" || report.DeferredNoActiveSuperadmin || report.Ownership != (upgrade.OwnershipReport{}) || report.Layout != (upgrade.LayoutReport{}) {
		t.Fatalf("shared report = %+v", report)
	}
	requireUpgradeOwner(t, db, &store.AssetRow{}, "id", "asset", "")
	requireUpgradeOwner(t, db, &store.ConversationRow{}, "id", "conversation", "")
	requireUpgradeOwner(t, db, &store.CronJob{}, "id", "cron", "")
	requireUpgradeOwner(t, db, &store.SyncTombstone{}, "id", "group", "")
	requireUpgradeSetting(t, db, store.LayoutKey, "layout", 6)
	requireUpgradeSetting(t, db, store.AIProfilesSettingKey, `{"version":1,"profiles":[],"activeId":null}`, 7)
	var targeted int64
	if err := db.Model(&store.Setting{}).Where(upgradeSettingKeysIn(store.LayoutSettingKeyForUser("admin"), store.AIProfilesSettingKeyForUser("admin"))).Count(&targeted).Error; err != nil {
		t.Fatal(err)
	}
	if targeted != 0 {
		t.Fatalf("shared migration created %d user-scoped settings", targeted)
	}
	if after := snapshotConflicts(t, db); !reflect.DeepEqual(after, before) {
		t.Fatalf("shared migration changed rows:\nbefore %+v\nafter  %+v", before, after)
	}
}
