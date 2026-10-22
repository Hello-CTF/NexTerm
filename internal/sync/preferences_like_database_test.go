package sync

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestCollectPreferencesEscapesLikePatternDatabase(t *testing.T) {
	fixture := dbtest.NewFixture(t)
	assertCollectPreferencesLiteralMatch(t, fixture.OpenStore(t))
	if fixture.Backend == store.BackendMySQL || fixture.Backend == store.BackendMariaDB {
		t.Run("no_backslash_escapes", func(t *testing.T) {
			isolated := dbtest.NewFixture(t)
			assertCollectPreferencesLiteralMatch(t, openPreferenceNoBackslashStore(t, isolated))
		})
	}
}

func assertCollectPreferencesLiteralMatch(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	engine := NewEngine(db, nil, nil)
	tests := []struct {
		name            string
		userID          string
		lookalikeUserID string
	}{
		{name: "escape", userID: "user!100", lookalikeUserID: "userX100"},
		{name: "percent", userID: "user%100", lookalikeUserID: "userX100"},
		{name: "underscore", userID: "user_100", lookalikeUserID: "userX100"},
		{name: "backslash", userID: `user\100`, lookalikeUserID: "user100"},
		{name: "chinese", userID: "用户甲", lookalikeUserID: "用户乙"},
		{name: "ascii_case", userID: "UserCase", lookalikeUserID: "usercase"},
		{name: "combined", userID: `用户!%_\100`, lookalikeUserID: "用户WXYZ100"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prefix := account.PreferenceUserKeyPrefix(test.userID)
			if err := db.SettingSet(ctx, prefix+"appearance.themeMode", `"dark"`); err != nil {
				t.Fatal(err)
			}
			lookalikePrefix := account.PreferenceUserKeyPrefix(test.lookalikeUserID)
			if err := db.SettingSet(ctx, lookalikePrefix+"appearance.themeMode", `"light"`); err != nil {
				t.Fatal(err)
			}
			objects := map[string]localObject{}
			if err := engine.collectPreferences(ctx, test.userID, objects); err != nil {
				t.Fatal(err)
			}
			if len(objects) != 1 {
				t.Fatalf("collectPreferences(%q) returned %d objects, want only the literal user's preference: %v", test.userID, len(objects), objects)
			}
			object, ok := objects[preferenceObjectPrefix+"appearance.themeMode"]
			if !ok {
				t.Fatalf("collectPreferences(%q) objects = %v, want appearance.themeMode", test.userID, objects)
			}
			var payload preferenceObject
			if err := json.Unmarshal(object.plaintext, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Key != "appearance.themeMode" || string(payload.Value) != `"dark"` {
				t.Fatalf("preference payload = %+v, want appearance.themeMode dark", payload)
			}
		})
	}
}

func openPreferenceNoBackslashStore(t *testing.T, fixture *dbtest.Fixture) *store.Store {
	t.Helper()
	config, err := mysqldriver.ParseDSN(fixture.DSN)
	if err != nil {
		t.Fatal(err)
	}
	if config.Params == nil {
		config.Params = map[string]string{}
	}
	config.Params["sql_mode"] = "NO_BACKSLASH_ESCAPES"
	db, err := store.OpenBackend(t.Context(), store.BackendConfig{Backend: fixture.Backend, DSN: config.FormatDSN()}, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var sqlMode string
	if err := db.DB().QueryRowContext(t.Context(), "SELECT @@SESSION.sql_mode").Scan(&sqlMode); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlMode, "NO_BACKSLASH_ESCAPES") {
		t.Fatalf("session sql_mode = %q, want NO_BACKSLASH_ESCAPES", sqlMode)
	}
	return db
}

// A synced device grant persists the guard's explicit permanence marker on every supported backend:
// payloads carry no permanence bit, so old peers stay compatible while the durable record lands as a
// permanent long-term authorization, and a re-grant after a revocation tombstone is permanent again.
// Whole-row rewrites normalize bystander records with the guard's own rule: a missing marker becomes
// explicit permanent, while an explicit false marker is preserved verbatim.
func TestApplyAIGrantDevicePermanenceDatabase(t *testing.T) {
	fixture := dbtest.NewFixture(t)
	db := fixture.OpenStore(t)
	ctx := context.Background()
	engine := NewEngine(db, nil, nil)

	if err := db.SettingSet(ctx, guard.DeviceGrantsSettingKey, `{"dev-legacy":{"deviceId":"dev-legacy","kinds":["terminal_write"],"updatedAt":50},"dev-explicit-false":{"deviceId":"dev-explicit-false","kinds":["db_query"],"permanent":false,"updatedAt":60}}`); err != nil {
		t.Fatal(err)
	}
	requireBystanders := func() {
		t.Helper()
		value, found, err := db.SettingGet(ctx, guard.DeviceGrantsSettingKey)
		if err != nil || !found {
			t.Fatalf("device grants row: found=%v err=%v", found, err)
		}
		requireGrantRecordPermanence(t, value, map[string]bool{"dev-legacy": true, "dev-explicit-false": false})
		grants, err := guard.NewGrants(ctx, db, nil)
		if err != nil {
			t.Fatal(err)
		}
		if grant, ok, err := grants.Get(ctx, "dev-legacy"); err != nil || !ok || !grant.Permanent {
			t.Fatalf("guard loads legacy bystander as %+v %v err=%v, want permanent", grant, ok, err)
		}
		if grant, ok, err := grants.Get(ctx, "dev-explicit-false"); err != nil || !ok || grant.Permanent {
			t.Fatalf("guard loads explicit-false bystander as %+v %v err=%v, want non-permanent", grant, ok, err)
		}
	}

	applyGrant := func(updatedAt int64) {
		t.Helper()
		payload, err := marshalObject(aiGrantDeviceObject{ID: "device/dev-1", DeviceID: "dev-1", Kinds: []string{"terminal_write"}, UpdatedAt: updatedAt})
		if err != nil {
			t.Fatal(err)
		}
		if applied, _ := engine.applyAIGrantDevice(ctx, payload, &SyncReport{}); !applied {
			t.Fatalf("grant payload %d was not applied", updatedAt)
		}
	}
	requirePermanent := func() {
		t.Helper()
		value, found, err := db.SettingGet(ctx, guard.DeviceGrantsSettingKey)
		if err != nil || !found {
			t.Fatalf("device grants row: found=%v err=%v", found, err)
		}
		requireGrantRecordPermanence(t, value, map[string]bool{"dev-1": true})
		grants, err := guard.NewGrants(ctx, db, nil)
		if err != nil {
			t.Fatal(err)
		}
		grant, ok, err := grants.Get(ctx, "dev-1")
		if err != nil || !ok || !grant.Permanent {
			t.Fatalf("guard loads synced grant as %+v %v err=%v, want permanent", grant, ok, err)
		}
	}

	applyGrant(100)
	requirePermanent()
	requireBystanders()
	if applied, _ := engine.applyAIGrantTombstone(ctx, "device/dev-1", 200, &SyncReport{}); !applied {
		t.Fatal("grant tombstone was not applied")
	}
	requireBystanders()
	grants, err := guard.NewGrants(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := grants.Get(ctx, "dev-1"); err != nil || ok {
		t.Fatalf("tombstoned grant kept: %v %v", ok, err)
	}
	applyGrant(300)
	requirePermanent()
	requireBystanders()
}
