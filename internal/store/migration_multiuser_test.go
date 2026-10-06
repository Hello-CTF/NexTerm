package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

var multiUserTables = []string{
	"app_user", "user_dek", "user_session", "user_device", "sync_credential",
	"user_setting", "device_enroll_code", "user_sync_object", "user_sync_head",
	"sync_tombstone", "sync_state",
}

func tableNames(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names[name] = true
	}
	return names
}

func TestMigration0014FreshInstall(t *testing.T) {
	db := testStore(t)
	names := tableNames(t, db.DB())
	for _, name := range multiUserTables {
		if !names[name] {
			t.Errorf("missing table %s", name)
		}
	}
	for _, name := range []string{"asset", "asset_group", "credential", "snippet"} {
		if !names[name] {
			t.Errorf("additive migration dropped legacy table %s", name)
		}
	}
	if names["sync_tokens"] {
		t.Errorf("sync_tokens must be dropped by migration 0020")
	}
	var count int
	if err := db.DB().QueryRow("SELECT count(*) FROM " + migrationsTable).Scan(&count); err != nil || count != 17 {
		t.Fatalf("migration count=%d err=%v", count, err)
	}
}

func applyMigrationsUpTo(t *testing.T, db *sql.DB, maxVersion int64) {
	t.Helper()
	all, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
    version BIGINT PRIMARY KEY,
    description TEXT NOT NULL,
    installed_on TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    checksum BLOB NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.version > maxVersion {
			continue
		}
		if _, err := db.Exec(string(m.sql)); err != nil {
			t.Fatalf("apply migration %d: %v", m.version, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version, description, checksum) VALUES(?,?,?)`,
			m.version, m.description, m.checksum); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigration0014UpgradeKeepsLocalData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	raw, err := openDB(sqliteDSN(path), 1)
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationsUpTo(t, raw, 13)
	legacy := []string{
		`INSERT INTO asset_group(id, name, sort, created_at, updated_at) VALUES('grp1', 'servers', 0, 1, 1)`,
		`INSERT INTO asset(id, group_id, kind, name, host, options_json, tags, note, sort, created_at, updated_at)
VALUES('ast1', 'grp1', 'ssh', 'prod', '192.0.2.10', '{}', '', '', 0, 1, 1)`,
		`INSERT INTO credential(id, name, kind, cipher, nonce, blob, kek_hint, created_at, updated_at)
VALUES('cred1', 'key', 'ssh_key', 'aes256gcm', X'00', X'00', 'master:0', 1, 1)`,
		`INSERT INTO snippet(id, name, body, sort, created_at, updated_at) VALUES('snp1', 'greet', 'echo hi', 0, 1, 1)`,
		`INSERT INTO setting(key, value, updated_at) VALUES('sync.token', 'legacy-token', 1)`,
		`INSERT INTO sync_tokens(id, client_id, purpose, secret_hash, created_at, expires_at)
VALUES('tok1', 'desktop', 'sync', 'hash1', 1, 0)`,
	}
	for _, statement := range legacy {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade open: %v", err)
	}
	defer func() { _ = db.Close() }()

	names := tableNames(t, db.DB())
	for _, name := range multiUserTables {
		if !names[name] {
			t.Errorf("missing table %s after upgrade", name)
		}
	}
	var count int
	if err := db.DB().QueryRow("SELECT count(*) FROM " + migrationsTable).Scan(&count); err != nil || count != 17 {
		t.Fatalf("migration count=%d err=%v", count, err)
	}
	var groupName, assetHost, credHint, snippetBody string
	if err := db.DB().QueryRow("SELECT name FROM asset_group WHERE id = 'grp1'").Scan(&groupName); err != nil || groupName != "servers" {
		t.Errorf("asset_group row lost: %q err=%v", groupName, err)
	}
	if err := db.DB().QueryRow("SELECT host FROM asset WHERE id = 'ast1'").Scan(&assetHost); err != nil || assetHost != "192.0.2.10" {
		t.Errorf("asset row lost: %q err=%v", assetHost, err)
	}
	if err := db.DB().QueryRow("SELECT kek_hint FROM credential WHERE id = 'cred1'").Scan(&credHint); err != nil || credHint != "master:0" {
		t.Errorf("credential row lost: %q err=%v", credHint, err)
	}
	if err := db.DB().QueryRow("SELECT body FROM snippet WHERE id = 'snp1'").Scan(&snippetBody); err != nil || snippetBody != "echo hi" {
		t.Errorf("snippet row lost: %q err=%v", snippetBody, err)
	}
	// 0020 清理令牌时代数据: sync.token 设置与 sync_tokens 表必须随之消失。
	var tokenSettings int
	if err := db.DB().QueryRow("SELECT count(*) FROM setting WHERE key = 'sync.token'").Scan(&tokenSettings); err != nil || tokenSettings != 0 {
		t.Errorf("token-era setting survived migration 0020: %d err=%v", tokenSettings, err)
	}
	if names := tableNames(t, db.DB()); names["sync_tokens"] {
		t.Errorf("sync_tokens survived migration 0020")
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen after upgrade: %v", err)
	}
	_ = reopened.Close()
}

func TestMigration0014RoleAndStateConstraints(t *testing.T) {
	db := testStore(t)
	if _, err := db.DB().Exec(`INSERT INTO app_user(id, username, role, password_hash, state, created_at, updated_at)
VALUES('u1', 'alice', 'admin', 'x', 'active', 1, 1)`); err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Errorf("role CHECK not enforced: %v", err)
	}
	if _, err := db.DB().Exec(`INSERT INTO app_user(id, username, role, password_hash, state, created_at, updated_at)
VALUES('u1', 'alice', 'user', 'x', 'bogus', 1, 1)`); err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Errorf("state CHECK not enforced: %v", err)
	}
	if _, err := db.DB().Exec(`INSERT INTO app_user(id, username, role, password_hash, state, created_at, updated_at)
VALUES('u1', 'alice', 'user', 'x', 'active', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`INSERT INTO app_user(id, username, role, password_hash, state, created_at, updated_at)
VALUES('u2', 'Alice', 'user', 'x', 'active', 1, 1)`); err == nil {
		t.Errorf("username NOCASE uniqueness not enforced")
	}
}

func TestMigration0014ForeignKeyCascade(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB().ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO app_user(id, username, role, password_hash, state, created_at, updated_at)
VALUES('u1', 'alice', 'superadmin', 'x', 'active', 1, 1)`)
	mustExec(`INSERT INTO user_dek(user_id, dek_envelope, kdf_salt, kdf_params, recovery_envelope, recovery_hash, created_at, updated_at)
VALUES('u1', X'01', X'02', '{}', X'03', 'h', 1, 1)`)
	mustExec(`INSERT INTO user_device(id, user_id, name, kind, created_at) VALUES('d1', 'u1', 'laptop', 'desktop', 1)`)
	mustExec(`INSERT INTO user_session(id, user_id, device_id, token_hash, created_at, touched_at, expires_at)
VALUES('s1', 'u1', 'd1', 'th1', 1, 1, 99)`)
	mustExec(`INSERT INTO sync_credential(id, user_id, device_id, purpose, secret_hash, created_at)
VALUES('c1', 'u1', 'd1', 'sync', 'sh1', 1)`)
	mustExec(`INSERT INTO user_setting(user_id, key, value, updated_at) VALUES('u1', 'k', 'v', 1)`)
	mustExec(`INSERT INTO device_enroll_code(id, user_id, code_hash, created_at, expires_at) VALUES('e1', 'u1', 'ch1', 1, 99)`)
	mustExec(`INSERT INTO user_sync_object(user_id, id, seq, blob) VALUES('u1', 'obj1', 1, X'04')`)
	mustExec(`INSERT INTO user_sync_head(user_id, head_hash) VALUES('u1', 'hh')`)

	mustExec(`DELETE FROM app_user WHERE id = 'u1'`)
	for table, column := range map[string]string{
		"user_dek": "user_id", "user_session": "user_id", "user_device": "user_id",
		"sync_credential": "user_id", "user_setting": "user_id",
		"device_enroll_code": "user_id", "user_sync_object": "user_id", "user_sync_head": "user_id",
	} {
		var count int
		if err := db.DB().QueryRow(fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = 'u1'", table, column)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("%s row survived user cascade", table)
		}
	}

	mustExec(`INSERT INTO app_user(id, username, role, password_hash, state, created_at, updated_at)
VALUES('u2', 'bob', 'user', 'x', 'active', 1, 1)`)
	mustExec(`INSERT INTO user_device(id, user_id, name, kind, created_at) VALUES('d2', 'u2', 'phone', 'mobile', 1)`)
	mustExec(`INSERT INTO user_session(id, user_id, device_id, token_hash, created_at, touched_at, expires_at)
VALUES('s2', 'u2', 'd2', 'th2', 1, 1, 99)`)
	mustExec(`INSERT INTO sync_credential(id, user_id, device_id, purpose, secret_hash, created_at)
VALUES('c2', 'u2', 'd2', 'sync', 'sh2', 1)`)
	mustExec(`DELETE FROM user_device WHERE id = 'd2'`)

	var deviceID sql.NullString
	if err := db.DB().QueryRow("SELECT device_id FROM user_session WHERE id = 's2'").Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	if deviceID.Valid {
		t.Errorf("session device_id not set null: %v", deviceID)
	}
	var credCount int
	if err := db.DB().QueryRow("SELECT count(*) FROM sync_credential WHERE id = 'c2'").Scan(&credCount); err != nil {
		t.Fatal(err)
	}
	if credCount != 0 {
		t.Errorf("sync_credential did not cascade on device delete")
	}

	if _, err := db.DB().Exec(`INSERT INTO user_session(id, user_id, token_hash, created_at, touched_at, expires_at)
VALUES('s3', 'missing', 'th3', 1, 1, 99)`); err == nil {
		t.Errorf("user_session accepted dangling user_id")
	}
}

func TestMigration0019FleetUpgradeFromPublishedBaseline(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rwig-baseline.db")
	raw, err := openDB(sqliteDSN(path), 1)
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationsUpTo(t, raw, 18)
	baseline := []string{
		`INSERT INTO app_user(id, username, role, password_hash, state, created_at, updated_at)
VALUES('u1', 'alice', 'superadmin', 'x', 'active', 1, 1)`,
		`INSERT INTO user_device(id, user_id, name, kind, created_at) VALUES('d1', 'u1', 'laptop', 'desktop', 1)`,
		`INSERT INTO sync_credential(id, user_id, device_id, purpose, secret_hash, created_at)
VALUES('c1', 'u1', 'd1', 'sync', 'sh1', 1)`,
	}
	for _, statement := range baseline {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade open: %v", err)
	}
	defer func() { _ = db.Close() }()

	names := tableNames(t, db.DB())
	for _, name := range []string{"device_agent", "device_metrics", "device_metrics_hourly"} {
		if !names[name] {
			t.Errorf("missing table %s after upgrade", name)
		}
	}
	var count int
	if err := db.DB().QueryRow("SELECT count(*) FROM " + migrationsTable).Scan(&count); err != nil || count != 17 {
		t.Fatalf("migration count=%d err=%v", count, err)
	}
	var username string
	if err := db.DB().QueryRow("SELECT username FROM app_user WHERE id = 'u1'").Scan(&username); err != nil || username != "alice" {
		t.Errorf("baseline user lost: %q err=%v", username, err)
	}
	if _, err := db.DB().Exec(`INSERT INTO device_agent(device_id, credential_id, platform, app_version, created_at)
VALUES('d1', 'c1', 'linux', '1.0.0', 1)`); err != nil {
		t.Fatalf("device_agent unusable after upgrade: %v", err)
	}
	if _, err := db.DB().Exec(`INSERT INTO device_metrics(id, device_id, ts, cpu_pct) VALUES('m1', 'd1', 1, 1.5)`); err != nil {
		t.Fatalf("device_metrics unusable after upgrade: %v", err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen after upgrade: %v", err)
	}
	_ = reopened.Close()
}
