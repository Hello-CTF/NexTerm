package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func requireOwnerOnlyMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s mode=%#o, want 0600", path, got)
	}
}

func TestDatabaseFilesAreOwnerOnlyAndRepairedOnReopen(t *testing.T) {
	if !ownerOnlyPermissionsSupported() {
		t.Skip("platform does not expose owner/group/other permission bits")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "data.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		requireOwnerOnlyMode(t, name)
	}
	if err := db.SettingSet(ctx, "key", "value"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		requireOwnerOnlyMode(t, name)
	}
	value, found, err := reopened.SettingGet(ctx, "key")
	if err != nil || !found || value != "value" {
		t.Fatalf("setting value=%q found=%v err=%v", value, found, err)
	}
}

func TestPrepareDatabaseFilePermissionsCreateOrRepairOwnerOnly(t *testing.T) {
	if !ownerOnlyPermissionsSupported() {
		t.Skip("platform does not expose owner/group/other permission bits")
	}
	dir := t.TempDir()

	created := filepath.Join(dir, "new.db")
	if err := prepareDatabaseFile(created); err != nil {
		t.Fatal(err)
	}
	requireOwnerOnlyMode(t, created)
	if info, err := os.Stat(created); err != nil || info.Size() != 0 {
		t.Fatalf("created file = %+v, %v; want an empty file", info, err)
	}

	existing := filepath.Join(dir, "existing.db")
	content := []byte("existing database content")
	if err := os.WriteFile(existing, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareDatabaseFile(existing); err != nil {
		t.Fatal(err)
	}
	requireOwnerOnlyMode(t, existing)
	if data, err := os.ReadFile(existing); err != nil || !bytes.Equal(data, content) {
		t.Fatalf("existing file content = %q, %v; want it unchanged", data, err)
	}

	if err := prepareDatabaseFile(filepath.Join(dir, "missing", "data.db")); err == nil {
		t.Fatal("a database path in a missing directory must fail")
	}
}
