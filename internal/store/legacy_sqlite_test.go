package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func createLegacyMarkerDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE schema_migrations (
		version BIGINT PRIMARY KEY,
		description TEXT NOT NULL,
		installed_on TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		checksum BLOB NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version, description, checksum) VALUES (1, 'init', X'00')`); err != nil {
		t.Fatal(err)
	}
}

func legacyBackups(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(path + ".legacy-*")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestOpenBacksUpLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	createLegacyMarkerDB(t, path)

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	backups := legacyBackups(t, path)
	if len(backups) != 1 {
		t.Fatalf("legacy backups = %v; want exactly one", backups)
	}
	if err := s.SettingSet(context.Background(), "k", "v"); err != nil {
		t.Fatal(err)
	}
	var name string
	err = s.DB().QueryRow(`SELECT name FROM sqlite_master WHERE name = 'schema_migrations'`).Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("new database still has schema_migrations marker: %v", err)
	}

	backup, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backup.Close() }()
	if err := backup.QueryRow(`SELECT name FROM sqlite_master WHERE name = 'schema_migrations'`).Scan(&name); err != nil {
		t.Fatalf("backup lost the legacy marker: %v", err)
	}
}

func TestOpenFreshDatabaseSkipsBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if backups := legacyBackups(t, path); len(backups) != 0 {
		t.Fatalf("fresh database produced backups: %v", backups)
	}
}
