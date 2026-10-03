package memory

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
)

func TestUpdateSettingsFlagsAndCAS(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)

	settings, err := store.Settings(ctx, testScope)
	if err != nil {
		t.Fatal(err)
	}
	if settings.InjectionEnabled || settings.ToolsEnabled || settings.Version != 0 {
		t.Fatalf("default settings = %+v", settings)
	}
	if _, err := store.UpdateSettings(ctx, testScope, SettingsInput{}, 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty update err = %v", err)
	}

	enabled := true
	settings, err = store.UpdateSettings(ctx, testScope, SettingsInput{InjectionEnabled: &enabled}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.InjectionEnabled || settings.ToolsEnabled || settings.Version != 1 {
		t.Fatalf("after injection flip = %+v", settings)
	}
	if _, err := store.UpdateSettings(ctx, testScope, SettingsInput{InjectionEnabled: &enabled}, 0); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version err = %v", err)
	}
	var conflict *VersionConflictError
	if _, err := store.UpdateSettings(ctx, testScope, SettingsInput{ToolsEnabled: &enabled}, 0); !errors.As(err, &conflict) || conflict.Expected != 0 || conflict.Actual != 1 {
		t.Fatalf("conflict detail = %+v", conflict)
	}

	settings, err = store.UpdateSettings(ctx, testScope, SettingsInput{ToolsEnabled: &enabled}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.InjectionEnabled || !settings.ToolsEnabled || settings.Version != 2 {
		t.Fatalf("after tools flip = %+v", settings)
	}
	// SetInjectionEnabled keeps working as the single-flag delegate.
	disabled := false
	settings, err = store.SetInjectionEnabled(ctx, testScope, disabled, 2)
	if err != nil {
		t.Fatal(err)
	}
	if settings.InjectionEnabled || !settings.ToolsEnabled || settings.Version != 3 {
		t.Fatalf("after delegate flip = %+v", settings)
	}
	// Settings are scoped: another scope still reads the defaults.
	other := Scope{Tenant: testScope.Tenant, Subject: "subject-b"}
	if settings, err := store.Settings(ctx, other); err != nil || settings.Version != 0 || settings.ToolsEnabled {
		t.Fatalf("other scope settings = %+v err=%v", settings, err)
	}
}

func TestUpdateSettingsConcurrentCASHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)
	enabled := true
	if _, err := store.UpdateSettings(ctx, testScope, SettingsInput{ToolsEnabled: &enabled}, 0); err != nil {
		t.Fatal(err)
	}
	const racers = 8
	var wg sync.WaitGroup
	results := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := store.UpdateSettings(ctx, testScope, SettingsInput{InjectionEnabled: &enabled}, 1)
			results[index] = err
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrVersionConflict) {
			t.Fatalf("unexpected update error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("CAS winners = %d, want exactly 1", winners)
	}
	settings, err := store.Settings(ctx, testScope)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Version != 2 {
		t.Fatalf("version after race = %d", settings.Version)
	}
}

// TestMigrationV1ToV2 proves an existing v1 database gains tools_enabled
// defaulting to off while the stored injection flag and version survive.
func TestMigrationV1ToV2(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/memory-v1.db"
	legacy, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE memory_entry (
			id TEXT PRIMARY KEY,
			tenant TEXT NOT NULL,
			subject TEXT NOT NULL,
			topic TEXT NOT NULL,
			content TEXT NOT NULL,
			version INTEGER NOT NULL CHECK(version > 0),
			redacted INTEGER NOT NULL CHECK(redacted IN (0, 1)),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE memory_settings (
			tenant TEXT NOT NULL,
			subject TEXT NOT NULL,
			injection_enabled INTEGER NOT NULL CHECK(injection_enabled IN (0, 1)),
			version INTEGER NOT NULL CHECK(version > 0),
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(tenant, subject)
		)`,
		`INSERT INTO memory_settings (tenant, subject, injection_enabled, version, updated_at)
			VALUES('tenant-a', 'subject-a', 1, 3, 1)`,
		"PRAGMA user_version = 1",
	}
	for _, statement := range statements {
		if _, err := legacy.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store := openMemoryStore(t, path)
	settings, err := store.Settings(ctx, testScope)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.InjectionEnabled || settings.ToolsEnabled || settings.Version != 3 {
		t.Fatalf("migrated settings = %+v", settings)
	}
	enabled := true
	settings, err = store.UpdateSettings(ctx, testScope, SettingsInput{ToolsEnabled: &enabled}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.InjectionEnabled || !settings.ToolsEnabled || settings.Version != 4 {
		t.Fatalf("post-migration update = %+v", settings)
	}
}
