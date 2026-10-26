package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func createPreGormDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range []string{
		`CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, description TEXT NOT NULL, checksum BLOB NOT NULL)`,
		`INSERT INTO schema_migrations (version, description, checksum) VALUES (1, 'init', X'00')`,
		`CREATE TABLE legacy_payload (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO legacy_payload (id, value) VALUES (1, 'must survive')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenRejectsPreGormDatabaseWithoutMigrating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	createPreGormDB(t, path)

	for attempt := 0; attempt < 2; attempt++ {
		s, err := Open(context.Background(), path)
		if s != nil {
			_ = s.Close()
			t.Fatal("pre-GORM open returned a store")
		}
		requireCode(t, err, ipc.CodeUnsupported)
		if !strings.Contains(err.Error(), "pre-GORM") {
			t.Fatalf("error does not name the unsupported database: %v", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var value string
	if err := db.QueryRow(`SELECT value FROM legacy_payload WHERE id = 1`).Scan(&value); err != nil || value != "must survive" {
		t.Fatalf("legacy data value=%q err=%v", value, err)
	}
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'app_user'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("GORM tables were created in the rejected database: count=%d err=%v", tables, err)
	}
}

func TestOpenFreshDatabaseAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "data.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SettingSet(ctx, "fresh", "created"); err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	value, found, err := reopened.SettingGet(ctx, "fresh")
	if err != nil || !found || value != "created" {
		t.Fatalf("value=%q found=%v err=%v", value, found, err)
	}
}

// Databases written by v1.2.8/v1.2.9 still carry the ai_owner_migration ledger
// that 1.3.0 no longer models; it must not block opening.
func TestOpenDatabaseWithRetiredOwnerMigrationTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SettingSet(ctx, "kept", "yes"); err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `CREATE TABLE ai_owner_migration (kind TEXT NOT NULL, row_id TEXT NOT NULL, PRIMARY KEY (kind, row_id))`); err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	value, found, err := reopened.SettingGet(ctx, "kept")
	if err != nil || !found || value != "yes" {
		t.Fatalf("value=%q found=%v err=%v", value, found, err)
	}
}

// A second handle opened while the first still holds the database in WAL mode
// (committed rows live only in the -wal file) must open and share writes.
func TestOpenDatabaseWithUncheckpointedWALWhileInUse(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	if err := first.SettingSet(ctx, "persisted", "yes"); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	value, found, err := second.SettingGet(ctx, "persisted")
	if err != nil || !found || value != "yes" {
		t.Fatalf("value=%q found=%v err=%v", value, found, err)
	}
	if err := second.SettingSet(ctx, "second", "write"); err != nil {
		t.Fatal(err)
	}
	if value, found, err := first.SettingGet(ctx, "second"); err != nil || !found || value != "write" {
		t.Fatalf("value=%q found=%v err=%v", value, found, err)
	}
}

func TestOpenCancelledContextReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, err := Open(ctx, path)
	if err == nil {
		_ = s.Close()
		t.Fatal("cancelled open unexpectedly succeeded")
	}
}
