package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func createLegacySchema(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE schema_migrations (
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
	if _, err := db.Exec(`CREATE TABLE legacy_payload (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO legacy_payload (id, value) VALUES (1, 'must survive')`); err != nil {
		t.Fatal(err)
	}
}

func createLegacyMarkerDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	createLegacySchema(t, db)
}

func createLegacyWALDB(t *testing.T, path string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatal(err)
	}
	createLegacySchema(t, db)
	copyDatabaseFiles(t, source, path)
}

func copyDatabaseFiles(t *testing.T, source, destination string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		content, err := os.ReadFile(source + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination+suffix, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

type databaseFileState struct {
	exists  bool
	content []byte
}

func databaseFileStates(t *testing.T, path string) map[string]databaseFileState {
	t.Helper()
	states := make(map[string]databaseFileState, 3)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		content, err := os.ReadFile(path + suffix)
		switch {
		case err == nil:
			states[suffix] = databaseFileState{exists: true, content: content}
		case os.IsNotExist(err):
			states[suffix] = databaseFileState{}
		default:
			t.Fatal(err)
		}
	}
	return states
}

func requireDatabaseFileStates(t *testing.T, path string, expected map[string]databaseFileState) {
	t.Helper()
	actual := databaseFileStates(t, path)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if actual[suffix].exists != expected[suffix].exists || !bytes.Equal(actual[suffix].content, expected[suffix].content) {
			t.Errorf("database file %s changed after rejected open", path+suffix)
		}
	}
}

func requirePreGormUnsupported(t *testing.T, path string) {
	t.Helper()
	s, err := Open(context.Background(), path)
	if s != nil {
		_ = s.Close()
		t.Fatal("pre-GORM open returned a store")
	}
	requireCode(t, err, ipc.CodeUnsupported)
	if !strings.Contains(err.Error(), "pre-GORM") || !strings.Contains(err.Error(), "v1.0.3") {
		t.Fatalf("unsupported error does not identify the pre-GORM/v1.0.3 boundary: %v", err)
	}
}

func TestLegacyMarkerPreflightIgnoresMissingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	legacy, err := hasLegacySchemaMarkerAtPath(context.Background(), path, defaultBusyTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		t.Fatal("missing path reported a legacy schema marker")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing path preflight created a database: %v", err)
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

func requireFileMode(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if actual := info.Mode().Perm(); actual != expected {
		t.Fatalf("database mode = %#o, want %#o", actual, expected)
	}
}

func TestOpenRejectsPreGormDatabaseAndPreservesMode(t *testing.T) {
	if !ownerOnlyPermissionsSupported() {
		t.Skip("platform does not expose owner/group/other permission bits")
	}
	path := filepath.Join(t.TempDir(), "data.db")
	createLegacyMarkerDB(t, path)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	before := databaseFileStates(t, path)

	requirePreGormUnsupported(t, path)
	requireDatabaseFileStates(t, path, before)
	requireFileMode(t, path, 0o644)
}

func TestOpenRejectsReadOnlyPreGormDatabase(t *testing.T) {
	if !ownerOnlyPermissionsSupported() {
		t.Skip("platform does not expose owner/group/other permission bits")
	}
	path := filepath.Join(t.TempDir(), "data.db")
	createLegacyMarkerDB(t, path)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	before := databaseFileStates(t, path)

	requirePreGormUnsupported(t, path)
	requireDatabaseFileStates(t, path, before)
	requireFileMode(t, path, 0o400)
}

func TestOpenRejectsPreGormDatabaseAndPreservesFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	createLegacyMarkerDB(t, path)
	before := databaseFileStates(t, path)

	requirePreGormUnsupported(t, path)
	requireDatabaseFileStates(t, path, before)

	requirePreGormUnsupported(t, path)
	requireDatabaseFileStates(t, path, before)
}

func TestOpenRejectsPreGormDatabaseAndPreservesSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	createLegacyWALDB(t, path)
	before := databaseFileStates(t, path)
	if !before["-wal"].exists || !before["-shm"].exists {
		t.Fatal("legacy WAL fixture did not preserve both sidecars")
	}

	requirePreGormUnsupported(t, path)
	requireDatabaseFileStates(t, path, before)
}

func TestOpenGORMDatabaseWithWALRemainsSupported(t *testing.T) {
	ctx := context.Background()
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	source, err := Open(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	if err := source.SettingSet(ctx, "persisted", "yes"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "data.db")
	copyDatabaseFiles(t, sourcePath, path)

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	value, found, err := s.SettingGet(ctx, "persisted")
	if err != nil || !found || value != "yes" {
		t.Fatalf("value=%q found=%v err=%v", value, found, err)
	}
}

func TestOpenRejectsPreGormDatabaseWithLegacyRenameCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	createLegacyWALDB(t, path)
	base := time.Now()
	for offset := 0; offset < 10; offset++ {
		stamp := base.Add(time.Duration(offset) * time.Second).Format("20060102-150405")
		if err := os.Mkdir(fmt.Sprintf("%s.legacy-%s-wal", path, stamp), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	before := databaseFileStates(t, path)

	requirePreGormUnsupported(t, path)
	requireDatabaseFileStates(t, path, before)
	for offset := 0; offset < 10; offset++ {
		stamp := base.Add(time.Duration(offset) * time.Second).Format("20060102-150405")
		info, err := os.Stat(fmt.Sprintf("%s.legacy-%s-wal", path, stamp))
		if err != nil || !info.IsDir() {
			t.Fatalf("legacy rename collision at offset %d changed: info=%v err=%v", offset, info, err)
		}
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
