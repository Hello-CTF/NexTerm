package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gorm.io/gorm"
)

var legacyDatabaseScope = Scope{Tenant: "local", Subject: "default"}
var legacyTargetScope = Scope{Tenant: "user", Subject: "superadmin"}

func legacyDatabaseEntry(id string, scope Scope, version int64, updatedAt int64, content string) entryRow {
	return entryRow{
		ID: id, Tenant: scope.Tenant, Subject: scope.Subject, Topic: "legacy", Content: content,
		Version: version, CreatedAt: updatedAt - 10, UpdatedAt: updatedAt,
	}
}

func writeLegacyDatabase(t *testing.T, path string, entries []entryRow, settings *settingsRow) {
	t.Helper()
	legacy, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = legacy.Close() })
	if len(entries) != 0 {
		if err := legacy.db.Create(&entries).Error; err != nil {
			t.Fatal(err)
		}
	}
	if settings != nil {
		if err := legacy.db.Create(settings).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
}

func requireMemoryRow(t *testing.T, db *gorm.DB, id string) entryRow {
	t.Helper()
	var row entryRow
	if err := db.Where("id = ?", id).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func TestMemoryLegacyImportConflictsDatabase(t *testing.T) {
	ctx := context.Background()
	store, db := openMemoryDatabase(t)
	path := filepath.Join(t.TempDir(), "memory.db")
	sourceEntries := []entryRow{
		legacyDatabaseEntry("insert", legacyDatabaseScope, 2, 200, "inserted"),
		legacyDatabaseEntry("update", legacyDatabaseScope, 3, 300, "source newer"),
		legacyDatabaseEntry("same", legacyDatabaseScope, 1, 100, "same"),
		legacyDatabaseEntry("target-newer", legacyDatabaseScope, 2, 200, "source older version"),
		legacyDatabaseEntry("target-timestamp", legacyDatabaseScope, 3, 200, "source older timestamp"),
		legacyDatabaseEntry("foreign-id", legacyDatabaseScope, 9, 900, "must not change another owner"),
	}
	sourceSettings := &settingsRow{
		Tenant: legacyDatabaseScope.Tenant, Subject: legacyDatabaseScope.Subject,
		InjectionEnabled: false, ToolsEnabled: true, Version: 2, UpdatedAt: 200,
	}
	writeLegacyDatabase(t, path, sourceEntries, sourceSettings)

	same := legacyDatabaseEntry("same", legacyTargetScope, 1, 100, "same")
	targetEntries := []entryRow{
		legacyDatabaseEntry("update", legacyTargetScope, 1, 100, "target older"),
		same,
		legacyDatabaseEntry("target-newer", legacyTargetScope, 4, 100, "target newer version"),
		legacyDatabaseEntry("target-timestamp", legacyTargetScope, 2, 300, "target newer timestamp"),
		legacyDatabaseEntry("foreign-id", Scope{Tenant: "user", Subject: "other"}, 1, 100, "other owner"),
	}
	if err := db.Create(&targetEntries).Error; err != nil {
		t.Fatal(err)
	}
	targetSettings := settingsRow{
		Tenant: legacyTargetScope.Tenant, Subject: legacyTargetScope.Subject,
		InjectionEnabled: true, ToolsEnabled: false, Version: 7, UpdatedAt: 700,
	}
	if err := db.Create(&targetSettings).Error; err != nil {
		t.Fatal(err)
	}

	report, err := store.ImportLegacySidecar(ctx, path, legacyDatabaseScope, legacyTargetScope)
	if err != nil {
		t.Fatal(err)
	}
	if !report.SourceFound || report.EntriesInserted != 1 || report.EntriesUpdated != 1 ||
		report.EntriesSkipped != 4 || report.EntryConflicts != 3 || !report.SettingsPreserved || report.SettingsImported {
		t.Fatalf("import report = %+v", report)
	}
	if report.ArchivePath != path+legacyArchiveSuffix {
		t.Fatalf("archive path = %q", report.ArchivePath)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy source still exists: %v", err)
	}
	if _, err := os.Stat(report.ArchivePath); err != nil {
		t.Fatalf("archived source is missing: %v", err)
	}
	for _, expected := range []entryRow{sourceEntries[0], sourceEntries[1], same, targetEntries[2], targetEntries[3], targetEntries[4]} {
		if expected.ID == sourceEntries[0].ID || expected.ID == sourceEntries[1].ID {
			expected.Tenant = legacyTargetScope.Tenant
			expected.Subject = legacyTargetScope.Subject
		}
		if actual := requireMemoryRow(t, db, expected.ID); actual != expected {
			t.Errorf("row %s = %+v, want %+v", expected.ID, actual, expected)
		}
	}
	var actualSettings settingsRow
	if err := db.Where("tenant = ? AND subject = ?", legacyTargetScope.Tenant, legacyTargetScope.Subject).First(&actualSettings).Error; err != nil {
		t.Fatal(err)
	}
	if actualSettings != targetSettings {
		t.Fatalf("target settings were overwritten: %+v, want %+v", actualSettings, targetSettings)
	}

	repeated, err := store.ImportLegacySidecar(ctx, path, legacyDatabaseScope, legacyTargetScope)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.SourceFound || repeated.EntriesInserted != 0 || repeated.EntriesUpdated != 0 || repeated.EntryConflicts != 0 {
		t.Fatalf("repeat after archive = %+v", repeated)
	}
	if actual := requireMemoryRow(t, db, "update"); actual.Version != 3 || actual.Content != "source newer" {
		t.Fatalf("repeat changed imported row: %+v", actual)
	}
}

func TestMemoryLegacyImportRollbackAndRetryDatabase(t *testing.T) {
	ctx := context.Background()
	store, db := openMemoryDatabase(t)
	path := filepath.Join(t.TempDir(), "memory.db")
	entries := []entryRow{legacyDatabaseEntry("retry", legacyDatabaseScope, 2, 200, "retry import")}
	settings := &settingsRow{
		Tenant: legacyDatabaseScope.Tenant, Subject: legacyDatabaseScope.Subject,
		InjectionEnabled: false, ToolsEnabled: false, Version: 3, UpdatedAt: 300,
	}
	writeLegacyDatabase(t, path, entries, settings)
	if err := db.Migrator().DropTable(&settingsRow{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportLegacySidecar(ctx, path, legacyDatabaseScope, legacyTargetScope); err == nil {
		t.Fatal("import with missing settings table succeeded")
	}
	var count int64
	if err := db.Model(&entryRow{}).Where("id = ?", "retry").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed import committed %d entries", count)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("failed import removed source: %v", err)
	}
	if _, err := os.Stat(path + legacyArchiveSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed import created archive: %v", err)
	}
	if err := db.AutoMigrate(&settingsRow{}); err != nil {
		t.Fatal(err)
	}
	report, err := store.ImportLegacySidecar(ctx, path, legacyDatabaseScope, legacyTargetScope)
	if err != nil {
		t.Fatal(err)
	}
	if report.EntriesInserted != 1 || !report.SettingsImported || report.EntriesSkipped != 0 {
		t.Fatalf("retry report = %+v", report)
	}
	expected := entries[0]
	expected.Tenant = legacyTargetScope.Tenant
	expected.Subject = legacyTargetScope.Subject
	if actual := requireMemoryRow(t, db, expected.ID); actual != expected {
		t.Fatalf("retried row = %+v, want %+v", actual, expected)
	}
}
