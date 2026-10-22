package production

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func writeProductionLegacyMemory(t *testing.T, path, content string) memory.Entry {
	t.Helper()
	legacy, err := memory.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := legacy.Create(context.Background(), productionMemoryScope, memory.CreateInput{Topic: "legacy", Content: content})
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := legacy.UpdateSettings(context.Background(), productionMemoryScope, memory.SettingsInput{ToolsEnabled: &disabled}, 0); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	return entry
}

func putProductionMemoryMigrationUser(t *testing.T, database *store.Store, id, role, state string, createdAt int64) {
	t.Helper()
	if err := database.GORM().Create(&store.AppUser{
		ID: id, Username: id, Role: role, State: state, PasswordHash: "test", CreatedAt: createdAt, UpdatedAt: createdAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestProductionMemoryFreshMainDatabaseHasNoSidecarDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	dataDir := t.TempDir()
	memoryStore, err := prepareProductionMemory(context.Background(), &ProductionServices{Store: database, dataDir: dataDir, desktop: false})
	if err != nil {
		t.Fatal(err)
	}
	if memoryStore == nil {
		t.Fatal("main database memory store was not configured")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "memory.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh startup created memory.db: %v", err)
	}
	if err := memoryStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().WithContext(context.Background()).Create(&store.AppUser{
		ID: "after-close", Username: "after-close", Role: "user", State: "active", PasswordHash: "test", CreatedAt: 1, UpdatedAt: 1,
	}).Error; err != nil {
		t.Fatalf("borrowed store closed the shared main database: %v", err)
	}
}

func TestProductionMemoryLegacyMigrationDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	memoryStore, err := memory.NewStore(database.GORM())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "memory.db")
	legacy := writeProductionLegacyMemory(t, path, "migrate-to-first-admin")
	putProductionMemoryMigrationUser(t, database, "disabled-admin", "superadmin", "disabled", 1)
	putProductionMemoryMigrationUser(t, database, "later-admin", "superadmin", "active", 20)
	putProductionMemoryMigrationUser(t, database, "first-admin", "superadmin", "active", 10)
	target := memory.Scope{Tenant: "user", Subject: "first-admin"}
	disabled := false
	if _, err := memoryStore.UpdateSettings(context.Background(), target, memory.SettingsInput{InjectionEnabled: &disabled}, 0); err != nil {
		t.Fatal(err)
	}
	report, err := migrateProductionLegacyMemory(context.Background(), database.GORM(), memoryStore, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Import.SourceFound || report.Import.EntriesInserted != 1 || report.Import.EntriesUpdated != 0 ||
		report.Import.EntryConflicts != 0 || !report.Import.SettingsPreserved || report.Import.SettingsImported {
		t.Fatalf("migration report = %+v", report)
	}
	entry, err := memoryStore.Get(context.Background(), target, legacy.ID)
	if err != nil || entry.Content != "migrate-to-first-admin" || entry.Version != legacy.Version || entry.CreatedAt != legacy.CreatedAt || entry.UpdatedAt != legacy.UpdatedAt {
		t.Fatalf("migrated entry = %+v, err = %v", entry, err)
	}
	settings, err := memoryStore.Settings(context.Background(), target)
	if err != nil || settings.InjectionEnabled || !settings.ToolsEnabled || settings.Version != 1 {
		t.Fatalf("target settings were not preserved: %+v, err = %v", settings, err)
	}
	for _, scope := range []memory.Scope{productionMemoryScope, {Tenant: "user", Subject: "later-admin"}} {
		if _, err := memoryStore.Get(context.Background(), scope, legacy.ID); err == nil {
			t.Fatalf("legacy entry was also assigned to %+v", scope)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source remains after archive: %v", err)
	}
	if _, err := os.Stat(path + ".migrated"); err != nil {
		t.Fatalf("original sidecar was not preserved: %v", err)
	}
	repeated, err := migrateProductionLegacyMemory(context.Background(), database.GORM(), memoryStore, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Import.SourceFound || repeated.Import.EntriesInserted != 0 || repeated.Import.EntriesUpdated != 0 || repeated.Import.EntryConflicts != 0 {
		t.Fatalf("idempotent reentry = %+v", repeated)
	}
	index, err := memoryStore.Index(context.Background(), target)
	if err != nil || len(index) != 1 || len(index[0].Entries) != 1 {
		t.Fatalf("reentry duplicated memory: %+v, err = %v", index, err)
	}
}

func TestProductionMemoryLegacyMigrationDefersWithoutActiveSuperadminDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "memory.db")
	legacy := writeProductionLegacyMemory(t, path, "wait-for-admin")
	putProductionMemoryMigrationUser(t, database, "regular-user", "user", "active", 1)
	var events []ipc.Event
	services := &ProductionServices{Store: database, dataDir: dataDir, desktop: false, Events: ipc.EmitterFunc(func(_ context.Context, event ipc.Event) error {
		events = append(events, event)
		return nil
	})}
	if _, err := prepareProductionMemory(context.Background(), services); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Event != ipc.TopicAppError {
		t.Fatalf("missing visible migration deferral: %+v", events)
	}
	payload, ok := events[0].Payload.(ipc.AppErrorEvent)
	if !ok || payload.Code != "memory_migration" || !strings.Contains(payload.Message, "active superadmin") {
		t.Fatalf("migration deferral payload = %+v", events[0].Payload)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("deferred migration removed source: %v", err)
	}
	if _, err := os.Stat(path + ".migrated"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deferred migration archived source: %v", err)
	}
	putProductionMemoryMigrationUser(t, database, "first-admin", "superadmin", "active", 2)
	memoryStore, err := prepareProductionMemory(context.Background(), services)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := memoryStore.Get(context.Background(), memory.Scope{Tenant: "user", Subject: "first-admin"}, legacy.ID)
	if err != nil || entry.Content != "wait-for-admin" {
		t.Fatalf("retry after admin creation = %+v, err = %v", entry, err)
	}
	if len(events) != 1 {
		t.Fatalf("successful retry emitted another deferral: %+v", events)
	}
}

func TestProductionMemoryArchiveOnlyDoesNotRequireSuperadminDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "memory.db")
	writeProductionLegacyMemory(t, path, "already-imported")
	putProductionMemoryMigrationUser(t, database, "regular-user", "user", "active", 1)
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(path+suffix, []byte("residual"+suffix), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(path, path+".migrated"); err != nil {
		t.Fatal(err)
	}
	memoryStore, err := memory.NewStore(database.GORM())
	if err != nil {
		t.Fatal(err)
	}
	report, err := migrateProductionLegacyMemory(context.Background(), database.GORM(), memoryStore, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.SkippedNoActiveSuperadmin || report.Import.SourceFound || report.Import.ArchivePath != path+".migrated" {
		t.Fatalf("archive-only migration report = %+v", report)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("archive-only source %s remains: %v", suffix, err)
		}
		content, err := os.ReadFile(path + ".migrated" + suffix)
		if err != nil || string(content) != "residual"+suffix {
			t.Fatalf("archived sidecar %s = %q, err = %v", suffix, content, err)
		}
	}
	archivedBefore, err := os.ReadFile(path + ".migrated")
	if err != nil {
		t.Fatal(err)
	}
	var events []ipc.Event
	services := &ProductionServices{Store: database, dataDir: dataDir, desktop: false, Events: ipc.EmitterFunc(func(_ context.Context, event ipc.Event) error {
		events = append(events, event)
		return nil
	})}
	if _, err := prepareProductionMemory(context.Background(), services); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("archive-only reentry emitted a migration deferral: %+v", events)
	}
	archivedAfter, err := os.ReadFile(path + ".migrated")
	if err != nil || string(archivedAfter) != string(archivedBefore) {
		t.Fatalf("archive-only reentry changed the archived database: %v", err)
	}
}
