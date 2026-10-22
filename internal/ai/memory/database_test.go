package memory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"gorm.io/gorm"
)

func openMemoryDatabase(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	backend := dbtest.NewFixture(t).OpenStore(t)
	store, err := NewStore(backend.GORM())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, backend.GORM()
}

func TestMemoryCRUDAndSettingsCASDatabase(t *testing.T) {
	ctx := context.Background()
	store, db := openMemoryDatabase(t)
	if !db.Migrator().HasIndex(&model.MemoryEntry{}, "memory_entry_scope_topic") {
		t.Fatal("memory scope/topic index is missing")
	}
	settings, err := store.Settings(ctx, testScope)
	if err != nil || settings != defaultSettings() {
		t.Fatalf("default settings = %+v, err = %v", settings, err)
	}
	disabled := false
	settings, err = store.UpdateSettings(ctx, testScope, SettingsInput{ToolsEnabled: &disabled}, 0)
	if err != nil || settings.ToolsEnabled || !settings.InjectionEnabled || settings.Version != 1 {
		t.Fatalf("updated settings = %+v, err = %v", settings, err)
	}
	if _, err := store.UpdateSettings(ctx, testScope, SettingsInput{ToolsEnabled: &disabled}, 0); err == nil {
		t.Fatal("stale settings update succeeded")
	} else {
		assertVersionConflict(t, err, 1)
	}
	entry := mustCreate(t, store, testScope, "database", "stored in the configured backend")
	got, err := store.Get(ctx, testScope, entry.ID)
	if err != nil || got != entry {
		t.Fatalf("entry = %+v, want %+v, err = %v", got, entry, err)
	}
	edited, err := store.Edit(ctx, testScope, entry.ID, 1, EditInput{Content: textPointer("edited")})
	if err != nil || edited.Version != 2 || edited.Content != "edited" {
		t.Fatalf("edited entry = %+v, err = %v", edited, err)
	}
	if _, err := store.Edit(ctx, testScope, entry.ID, 1, EditInput{Content: textPointer("stale")}); err == nil {
		t.Fatal("stale entry edit succeeded")
	} else {
		assertVersionConflict(t, err, 2)
	}
	index, err := store.Index(ctx, testScope)
	if err != nil || len(index) != 1 || len(index[0].Entries) != 1 || index[0].Entries[0].Version != 2 {
		t.Fatalf("index = %+v, err = %v", index, err)
	}
	other := Scope{Tenant: testScope.Tenant, Subject: "other"}
	if _, err := store.Get(ctx, other, entry.ID); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("cross-scope read err = %v", err)
	}
	if err := store.Delete(ctx, testScope, entry.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, testScope, entry.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted entry read err = %v", err)
	}
	wideScope := Scope{Tenant: strings.Repeat("t", maxScopeBytes), Subject: strings.Repeat("s", maxScopeBytes)}
	wideEntry := mustCreate(t, store, wideScope, strings.Repeat("n", MaxTopicBytes), "maximum indexed field sizes")
	if got, err := store.Get(ctx, wideScope, wideEntry.ID); err != nil || got != wideEntry {
		t.Fatalf("maximum-size indexed entry = %+v, err = %v", got, err)
	}
	if _, err := store.UpdateSettings(ctx, wideScope, SettingsInput{ToolsEnabled: &disabled}, 0); err != nil {
		t.Fatalf("maximum-size settings scope: %v", err)
	}
}

type settingsReadBarrier struct {
	arrived chan struct{}
	release chan struct{}
}

type settingsReadBarrierKey struct{}

type settingsCASResult struct {
	writer   int
	settings Settings
	err      error
}

func raceSettingsCAS(t *testing.T, store *Store, expected uint64, inputs [2]SettingsInput) [2]settingsCASResult {
	t.Helper()
	barrier := &settingsReadBarrier{arrived: make(chan struct{}, 8), release: make(chan struct{})}
	const callbackName = "memory:test_settings_read_barrier"
	if err := store.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(settingsReadBarrierKey{}) != barrier {
			return
		}
		select {
		case barrier.arrived <- struct{}{}:
		default:
		}
		<-barrier.release
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove settings read barrier: %v", err)
		}
	}()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer release()

	results := make(chan settingsCASResult, len(inputs))
	for index := range inputs {
		go func() {
			ctx := context.WithValue(context.Background(), settingsReadBarrierKey{}, barrier)
			settings, err := store.UpdateSettings(ctx, testScope, inputs[index], expected)
			results <- settingsCASResult{writer: index, settings: settings, err: err}
		}()
	}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for range inputs {
		select {
		case <-barrier.arrived:
		case result := <-results:
			t.Fatalf("settings update finished before the read barrier: %+v", result)
		case <-timer.C:
			t.Fatal("timed out waiting for concurrent settings reads")
		}
	}
	release()
	var pair [2]settingsCASResult
	for range inputs {
		select {
		case result := <-results:
			pair[result.writer] = result
		case <-timer.C:
			t.Fatal("timed out waiting for concurrent settings writes")
		}
	}
	return pair
}

func requireSettingsCASWinner(t *testing.T, results [2]settingsCASResult, version uint64) int {
	t.Helper()
	winner := -1
	for index, result := range results {
		if result.err == nil {
			if winner >= 0 {
				t.Fatalf("both settings writes succeeded: %+v", results)
			}
			if result.settings.Version != version {
				t.Fatalf("winner settings = %+v, want version %d", result.settings, version)
			}
			winner = index
			continue
		}
		assertVersionConflict(t, result.err, version)
	}
	if winner < 0 {
		t.Fatalf("neither settings write succeeded: %+v", results)
	}
	return winner
}

func TestMemorySettingsConcurrentInsertCASDatabase(t *testing.T) {
	store, _ := openMemoryDatabase(t)
	disabled := false
	results := raceSettingsCAS(t, store, 0, [2]SettingsInput{
		{InjectionEnabled: &disabled},
		{ToolsEnabled: &disabled},
	})
	winner := requireSettingsCASWinner(t, results, 1)
	settings, err := store.Settings(context.Background(), testScope)
	if err != nil || settings.Version != 1 {
		t.Fatalf("settings after race = %+v, err = %v", settings, err)
	}
	if winner == 0 && (settings.InjectionEnabled || !settings.ToolsEnabled) {
		t.Fatalf("writer 0 result was not preserved: %+v", settings)
	}
	if winner == 1 && (!settings.InjectionEnabled || settings.ToolsEnabled) {
		t.Fatalf("writer 1 result was not preserved: %+v", settings)
	}
}

func TestMemorySettingsConcurrentUpdateCASDatabase(t *testing.T) {
	store, _ := openMemoryDatabase(t)
	disabled, enabled := false, true
	if _, err := store.UpdateSettings(context.Background(), testScope, SettingsInput{InjectionEnabled: &disabled}, 0); err != nil {
		t.Fatal(err)
	}
	results := raceSettingsCAS(t, store, 1, [2]SettingsInput{
		{InjectionEnabled: &enabled},
		{ToolsEnabled: &disabled},
	})
	winner := requireSettingsCASWinner(t, results, 2)
	settings, err := store.Settings(context.Background(), testScope)
	if err != nil || settings.Version != 2 {
		t.Fatalf("settings after race = %+v, err = %v", settings, err)
	}
	if winner == 0 && (!settings.InjectionEnabled || !settings.ToolsEnabled) {
		t.Fatalf("writer 0 result was not preserved: %+v", settings)
	}
	if winner == 1 && (settings.InjectionEnabled || settings.ToolsEnabled) {
		t.Fatalf("writer 1 result was not preserved: %+v", settings)
	}
}

func TestMemoryBorrowedConnectionCloseDatabase(t *testing.T) {
	store, db := openMemoryDatabase(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	entry := mustCreate(t, store, testScope, "borrowed", "main database remains open")
	if got, err := store.Get(context.Background(), testScope, entry.ID); err != nil || got != entry {
		t.Fatalf("read after borrowed close = %+v, err = %v", got, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("borrowed store closed the main database: %v", err)
	}
	if _, err := NewStore(nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil database constructor err = %v", err)
	}
}

func TestMemorySQLiteStoreOwnsConnection(t *testing.T) {
	store, _ := newMemoryStore(t)
	sqlDB, err := store.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Ping(); err == nil {
		t.Fatal("standalone SQLite store did not close its owned connection")
	}
}
