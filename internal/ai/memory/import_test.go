package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryLegacyImportArchiveFailureRetryDatabase(t *testing.T) {
	ctx := context.Background()
	store, _ := openMemoryDatabase(t)
	path := filepath.Join(t.TempDir(), "memory.db")
	entries := []entryRow{legacyDatabaseEntry("committed", legacyDatabaseScope, 2, 200, "committed before archive")}
	settings := &settingsRow{
		Tenant: legacyDatabaseScope.Tenant, Subject: legacyDatabaseScope.Subject,
		InjectionEnabled: false, ToolsEnabled: true, Version: 3, UpdatedAt: 300,
	}
	writeLegacyDatabase(t, path, entries, settings)
	archiveErr := errors.New("injected archive interruption")
	report, err := store.importLegacySidecar(ctx, path, legacyDatabaseScope, legacyTargetScope, func(string) (string, error) {
		return "", archiveErr
	})
	if !errors.Is(err, archiveErr) {
		t.Fatalf("archive interruption err = %v", err)
	}
	if report.EntriesInserted != 1 || !report.SettingsImported {
		t.Fatalf("committed import report = %+v", report)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("archive failure removed source: %v", err)
	}
	if _, err := os.Stat(path + legacyArchiveSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive failure created archive: %v", err)
	}
	expected := entries[0]
	expected.Tenant = legacyTargetScope.Tenant
	expected.Subject = legacyTargetScope.Subject
	if actual := requireMemoryRow(t, store.db, expected.ID); actual != expected {
		t.Fatalf("committed row = %+v, want %+v", actual, expected)
	}

	retry, err := store.ImportLegacySidecar(ctx, path, legacyDatabaseScope, legacyTargetScope)
	if err != nil {
		t.Fatal(err)
	}
	if retry.EntriesInserted != 0 || retry.EntriesUpdated != 0 || retry.EntriesSkipped != 1 ||
		retry.EntryConflicts != 0 || !retry.SettingsPreserved || retry.SettingsImported {
		t.Fatalf("idempotent retry report = %+v", retry)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry left source in place: %v", err)
	}
	if _, err := os.Stat(path + legacyArchiveSuffix); err != nil {
		t.Fatalf("retry did not archive source: %v", err)
	}
}

func TestLegacyImportArchivesWALAndSHMAfterCommit(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)
	path := filepath.Join(t.TempDir(), "memory.db")
	entries := []entryRow{legacyDatabaseEntry("wal", legacyDatabaseScope, 1, 100, "wal archive")}
	writeLegacyDatabase(t, path, entries, nil)
	report, err := store.importLegacySidecar(ctx, path, legacyDatabaseScope, legacyTargetScope, func(path string) (string, error) {
		for suffix, content := range map[string][]byte{"-wal": []byte("wal-content"), "-shm": []byte("shm-content")} {
			if err := os.WriteFile(path+suffix, content, 0o600); err != nil {
				return "", err
			}
		}
		return archiveLegacySidecar(path)
	})
	if err != nil {
		t.Fatal(err)
	}
	for suffix, expected := range map[string][]byte{"": []byte(nil), "-wal": []byte("wal-content"), "-shm": []byte("shm-content")} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("source %s still exists: %v", suffix, err)
		}
		content, err := os.ReadFile(report.ArchivePath + suffix)
		if err != nil {
			t.Errorf("read archived %s: %v", suffix, err)
			continue
		}
		if suffix != "" && string(content) != string(expected) {
			t.Errorf("archived %s = %q, want %q", suffix, content, expected)
		}
	}
}

func TestLegacyImportMissingFileDoesNotCreateDatabase(t *testing.T) {
	store, _ := newMemoryStore(t)
	path := filepath.Join(t.TempDir(), "missing", "memory.db")
	report, err := store.ImportLegacySidecar(context.Background(), path, legacyDatabaseScope, legacyTargetScope)
	if err != nil {
		t.Fatal(err)
	}
	if report != (LegacyImportReport{}) {
		t.Fatalf("missing source report = %+v", report)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing import created parent directory: %v", err)
	}
	if _, err := store.ImportLegacySidecar(context.Background(), "", legacyDatabaseScope, legacyTargetScope); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty path err = %v", err)
	}
}

func TestLegacyArchiveFailureRestoresWholeGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	contents := map[string][]byte{"": []byte("main"), "-wal": []byte("wal"), "-shm": []byte("shm")}
	for suffix, content := range contents {
		if err := os.WriteFile(path+suffix, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	archivePath := path + legacyArchiveSuffix
	if err := os.WriteFile(archivePath+"-shm", []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveLegacySidecar(path); err == nil {
		t.Fatal("archive with occupied SHM destination succeeded")
	}
	for suffix, expected := range contents {
		actual, err := os.ReadFile(path + suffix)
		if err != nil || string(actual) != string(expected) {
			t.Errorf("restored source %s = %q, err = %v", suffix, actual, err)
		}
	}
	for _, suffix := range []string{"", "-wal"} {
		if _, err := os.Stat(archivePath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("failed archive left destination %s: %v", suffix, err)
		}
	}
	if err := os.Remove(archivePath + "-shm"); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveLegacySidecar(path); err != nil {
		t.Fatal(err)
	}
	for suffix, expected := range contents {
		actual, err := os.ReadFile(archivePath + suffix)
		if err != nil || string(actual) != string(expected) {
			t.Errorf("archived %s = %q, err = %v", suffix, actual, err)
		}
	}
}

func TestLegacyImportResumesPartialArchive(t *testing.T) {
	store, _ := newMemoryStore(t)
	path := filepath.Join(t.TempDir(), "memory.db")
	archivePath := path + legacyArchiveSuffix
	if err := os.WriteFile(archivePath, []byte("already archived"), 0o600); err != nil {
		t.Fatal(err)
	}
	for suffix, content := range map[string][]byte{"-wal": []byte("wal"), "-shm": []byte("shm")} {
		if err := os.WriteFile(path+suffix, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := store.ImportLegacySidecar(context.Background(), path, legacyDatabaseScope, legacyTargetScope)
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceFound || report.ArchivePath != archivePath {
		t.Fatalf("partial archive report = %+v", report)
	}
	for suffix, expected := range map[string][]byte{"": []byte("already archived"), "-wal": []byte("wal"), "-shm": []byte("shm")} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("partial source %s remains: %v", suffix, err)
		}
		actual, err := os.ReadFile(archivePath + suffix)
		if err != nil || string(actual) != string(expected) {
			t.Errorf("resumed archive %s = %q, err = %v", suffix, actual, err)
		}
	}
}

func TestLegacyImportRejectsUnmappedScopesWithoutArchiving(t *testing.T) {
	store, _ := newMemoryStore(t)
	path := filepath.Join(t.TempDir(), "memory.db")
	entries := []entryRow{
		legacyDatabaseEntry("mapped", legacyDatabaseScope, 1, 100, "mapped"),
		legacyDatabaseEntry("unmapped", Scope{Tenant: "user", Subject: "other"}, 1, 100, "unmapped"),
	}
	writeLegacyDatabase(t, path, entries, nil)
	if _, err := store.ImportLegacySidecar(context.Background(), path, legacyDatabaseScope, legacyTargetScope); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unmapped scope import err = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("rejected import removed source: %v", err)
	}
	var count int64
	if err := store.db.Model(&entryRow{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rejected import wrote %d rows, err = %v", count, err)
	}
}
