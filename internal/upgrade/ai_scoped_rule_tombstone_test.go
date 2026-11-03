package upgrade_test

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/upgrade"
)

func TestMigrateOpenModeRulesHonourScopedTombstoneIDs(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	db := database.GORM()
	putUpgradeUser(t, db, "admin", "superadmin", "active", 1)
	rules := `[` +
		`{"id":"scoped-gone","deviceId":"d","action":"exec_commands","kinds":["service"],"createdAt":4,"updatedAt":4},` +
		`{"id":"scoped-kept","deviceId":"d","action":"exec_commands","kinds":["package"],"createdAt":4,"updatedAt":4},` +
		`{"id":"path-gone","deviceId":"d","action":"write_file","path":"/etc/**","createdAt":4,"updatedAt":4}]`
	for _, row := range []any{
		&store.Setting{Key: guard.GrantRulesSettingKey, Value: rules, UpdatedAt: 13},
		// A legacy-prefix tombstone must not hide a scoped rule that only shares the bare ID.
		&store.SyncTombstone{ID: "scoped-rule/scoped-gone", Kind: store.SyncTombstoneKindAIGrant, DeletedAt: 15},
		&store.SyncTombstone{ID: "rule/scoped-kept", Kind: store.SyncTombstoneKindAIGrant, DeletedAt: 15},
		&store.SyncTombstone{ID: "rule/path-gone", Kind: store.SyncTombstoneKindAIGrant, DeletedAt: 15},
	} {
		createUpgradeRow(t, db, row)
	}
	report, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.AISettings.GrantRules.Tombstoned != 2 || report.AISettings.GrantRules.Migrated != 1 {
		t.Fatalf("grant rule report = %+v", report.AISettings.GrantRules)
	}
	var ruleRows []guard.GrantRule
	readUpgradeJSONSetting(t, db, guard.GrantRulesSettingKey+".user.admin", &ruleRows)
	if len(ruleRows) != 1 || ruleRows[0].ID != "scoped-kept" {
		t.Fatalf("copied grant rules = %+v", ruleRows)
	}
}
