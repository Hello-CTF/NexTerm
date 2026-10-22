package upgrade_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/upgrade"
	"gorm.io/gorm"
)

type upgradeFaultSnapshot struct {
	assets        []store.AssetRow
	credentials   []store.CredentialRow
	conversations []store.ConversationRow
	cronJobs      []store.CronJob
	tombstones    []store.SyncTombstone
	settings      []store.Setting
	ledger        []store.AIOwnerMigration
}

func snapshotUpgradeFault(t *testing.T, database *gorm.DB) upgradeFaultSnapshot {
	t.Helper()
	var snapshot upgradeFaultSnapshot
	for _, query := range []struct {
		dest  any
		order []string
	}{
		{&snapshot.assets, []string{"id"}},
		{&snapshot.credentials, []string{"id"}},
		{&snapshot.conversations, []string{"id"}},
		{&snapshot.cronJobs, []string{"id"}},
		{&snapshot.tombstones, []string{"owner_id", "id"}},
		{&snapshot.settings, []string{"key"}},
		{&snapshot.ledger, []string{"migration", "kind", "record_id"}},
	} {
		statement := database
		for _, column := range query.order {
			if column == "key" {
				statement = statement.Clauses(upgradeSettingOrder())
				continue
			}
			statement = statement.Order(column)
		}
		if err := statement.Find(query.dest).Error; err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func seedUpgradeFault(t *testing.T, database *gorm.DB) {
	t.Helper()
	putUpgradeUser(t, database, "admin", "superadmin", "active", 1)
	for _, row := range []any{
		&store.AssetRow{ID: "asset", Kind: "ssh", Name: "asset", OptionsJSON: "{}", CreatedAt: 2, UpdatedAt: 2},
		&store.CredentialRow{ID: "credential", Name: "credential", Kind: "password", Cipher: "opaque", Nonce: []byte{1}, Blob: []byte{2}, KEKHint: "hint", CreatedAt: 3, UpdatedAt: 3},
		&store.ConversationRow{ID: "conversation", ScopeJSON: "{}", CreatedAt: 4, UpdatedAt: 4},
		&store.CronJob{ID: "cron", SessionID: "conversation", Prompt: "prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: true, TimeoutMS: 1000, CreatedAt: 5, UpdatedAt: 5, Revision: 1, NextRunAt: 1},
		&store.SyncTombstone{ID: "deleted-group", Kind: "group", DeletedAt: 6},
		&store.Setting{Key: store.LayoutKey, Value: "layout", UpdatedAt: 7},
		&store.Setting{Key: store.AIProfilesSettingKey, Value: `{"version":1,"profiles":[{"id":"profile","name":"Profile","apiKey":"opaque-ai","model":"model"}],"activeId":"profile"}`, UpdatedAt: 8},
	} {
		createUpgradeRow(t, database, row)
	}
}

func armUpgradeFault(t *testing.T, database *gorm.DB, verb, table string, hits int) func() {
	t.Helper()
	remaining := hits
	callbackName := "nexterm_test:upgrade_fault"
	callback := func(tx *gorm.DB) {
		if remaining <= 0 || tx.Statement.Table != table {
			return
		}
		remaining--
		if remaining == 0 {
			tx.AddError(errors.New("forced data upgrade write failure"))
		}
	}
	var err error
	switch verb {
	case "create":
		err = database.Callback().Create().Before("gorm:create").Register(callbackName, callback)
	case "update":
		err = database.Callback().Update().Before("gorm:update").Register(callbackName, callback)
	default:
		t.Fatalf("unknown fault verb %q", verb)
	}
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	disarm := func() {
		once.Do(func() {
			switch verb {
			case "create":
				_ = database.Callback().Create().Remove(callbackName)
			case "update":
				_ = database.Callback().Update().Remove(callbackName)
			}
		})
	}
	t.Cleanup(disarm)
	return disarm
}

func TestMigrateFaultRollbackAndReentryDatabase(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		verb  string
		table string
		hits  int
	}{
		{name: "Ownership", verb: "update", table: "asset", hits: 1},
		{name: "Credential", verb: "update", table: "credential", hits: 1},
		{name: "Sync", verb: "update", table: "sync_tombstone", hits: 1},
		{name: "Layout", verb: "create", table: "setting", hits: 1},
		{name: "AIHistory", verb: "update", table: "cron_job", hits: 1},
		{name: "AISettings", verb: "create", table: "setting", hits: 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			database := dbtest.NewFixture(t).OpenStore(t)
			db := database.GORM()
			seedUpgradeFault(t, db)
			before := snapshotUpgradeFault(t, db)
			disarm := armUpgradeFault(t, db, scenario.verb, scenario.table, scenario.hits)

			report, err := upgrade.Migrate(context.Background(), db, false)
			disarm()
			if err == nil {
				t.Fatal("faulted migration succeeded")
			}
			if report != (upgrade.Report{}) {
				t.Fatalf("faulted report = %+v, want zero", report)
			}
			if after := snapshotUpgradeFault(t, db); !reflect.DeepEqual(after, before) {
				t.Fatalf("faulted migration was not rolled back:\nbefore %+v\nafter  %+v", before, after)
			}

			retried, err := upgrade.Migrate(context.Background(), db, false)
			if err != nil {
				t.Fatal(err)
			}
			if retried.Ownership.Assigned != 4 || retried.Layout.Copied != 1 || retried.AI.CronJobsAssigned != 1 || retried.AISettings.Profiles.Migrated != 1 {
				t.Fatalf("retry report = %+v", retried)
			}
			converged := snapshotUpgradeFault(t, db)
			for _, check := range []struct {
				model any
				id    string
			}{
				{&store.AssetRow{}, "asset"},
				{&store.CredentialRow{}, "credential"},
				{&store.ConversationRow{}, "conversation"},
				{&store.CronJob{}, "cron"},
				{&store.SyncTombstone{}, "deleted-group"},
			} {
				requireUpgradeOwner(t, db, check.model, "id", check.id, "admin")
			}
			requireUpgradeSetting(t, db, store.LayoutSettingKeyForUser("admin"), "layout", 7)

			repeated, err := upgrade.Migrate(context.Background(), db, false)
			if err != nil {
				t.Fatal(err)
			}
			if repeated.Ownership.Assigned != 0 || repeated.Layout.Existing != 1 || repeated.AI.Changed() || !repeated.AISettings.Empty() {
				t.Fatalf("idempotent reentry report = %+v", repeated)
			}
			if after := snapshotUpgradeFault(t, db); !reflect.DeepEqual(after, converged) {
				t.Fatalf("idempotent reentry changed rows:\nconverged %+v\nafter     %+v", converged, after)
			}
		})
	}
}
