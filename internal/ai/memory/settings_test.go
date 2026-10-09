package memory

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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
	if !settings.InjectionEnabled || !settings.ToolsEnabled || settings.Version != 0 {
		t.Fatalf("default settings = %+v", settings)
	}
	if _, err := store.UpdateSettings(ctx, testScope, SettingsInput{}, 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty update err = %v", err)
	}

	disabled := false
	settings, err = store.UpdateSettings(ctx, testScope, SettingsInput{InjectionEnabled: &disabled}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if settings.InjectionEnabled || !settings.ToolsEnabled || settings.Version != 1 {
		t.Fatalf("after injection flip = %+v", settings)
	}
	if _, err := store.UpdateSettings(ctx, testScope, SettingsInput{InjectionEnabled: &disabled}, 0); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version err = %v", err)
	}
	var conflict *VersionConflictError
	if _, err := store.UpdateSettings(ctx, testScope, SettingsInput{ToolsEnabled: &disabled}, 0); !errors.As(err, &conflict) || conflict.Expected != 0 || conflict.Actual != 1 {
		t.Fatalf("conflict detail = %+v", conflict)
	}

	settings, err = store.UpdateSettings(ctx, testScope, SettingsInput{ToolsEnabled: &disabled}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if settings.InjectionEnabled || settings.ToolsEnabled || settings.Version != 2 {
		t.Fatalf("after tools flip = %+v", settings)
	}

	enabled := true
	settings, err = store.SetInjectionEnabled(ctx, testScope, enabled, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.InjectionEnabled || settings.ToolsEnabled || settings.Version != 3 {
		t.Fatalf("after delegate flip = %+v", settings)
	}

	other := Scope{Tenant: testScope.Tenant, Subject: "subject-b"}
	if settings, err := store.Settings(ctx, other); err != nil || settings != defaultSettings() {
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

func TestMigrationV1IsRejected(t *testing.T) {
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

	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "unsupported semantic memory schema version 1") {
		t.Fatalf("v1 schema open err = %v", err)
	}
}
