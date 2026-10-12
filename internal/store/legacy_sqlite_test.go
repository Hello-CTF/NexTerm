package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
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

var legacyBackupName = regexp.MustCompile(`\.legacy-\d{8}-\d{6}$`)

func legacyBackups(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(path + ".legacy-*")
	if err != nil {
		t.Fatal(err)
	}
	var backups []string
	for _, match := range matches {
		if legacyBackupName.MatchString(match) {
			backups = append(backups, match)
		}
	}
	return backups
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

func TestBackupLegacyDatabaseMovesSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	createLegacyMarkerDB(t, path)
	for suffix, content := range map[string][]byte{"-wal": []byte("wal"), "-shm": []byte("shm")} {
		if err := os.WriteFile(path+suffix, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	backup, err := backupLegacyDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	backups := legacyBackups(t, path)
	if len(backups) != 1 || backups[0] != backup {
		t.Fatalf("legacy backups = %v; want only %s", backups, backup)
	}
	for suffix, expected := range map[string][]byte{"-wal": []byte("wal"), "-shm": []byte("shm")} {
		actual, err := os.ReadFile(backup + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if string(actual) != string(expected) {
			t.Fatalf("backup sidecar %s = %q; want %q", suffix, actual, expected)
		}
	}
}

func TestBackupLegacyDatabaseRollsBackPartialRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	createLegacyMarkerDB(t, path)
	if err := os.WriteFile(path+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	for offset := 0; offset < 10; offset++ {
		stamp := base.Add(time.Duration(offset) * time.Second).Format("20060102-150405")
		if err := os.Mkdir(fmt.Sprintf("%s.legacy-%s-wal", path, stamp), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := backupLegacyDatabase(path); err == nil {
		t.Fatal("partial legacy backup rename unexpectedly succeeded")
	}
	if backups := legacyBackups(t, path); len(backups) != 0 {
		t.Fatalf("rollback left legacy backups: %v", backups)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE name = 'schema_migrations'`).Scan(&name); err != nil {
		t.Fatalf("rollback lost the original database: %v", err)
	}
	wal, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if string(wal) != "wal" {
		t.Fatalf("rollback wal = %q; want wal", wal)
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
