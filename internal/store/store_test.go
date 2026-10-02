package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func fileStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "data.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func requireCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s", code)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected code %s, got %v", code, err)
	}
}

func ptr[T any](value T) *T { return &value }

func TestMigrationCreatesCompleteSchema(t *testing.T) {
	db := testStore(t)
	rows, err := db.DB().Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables[name] = true
	}
	for _, name := range []string{"asset_group", "asset", "credential", "setting", "audit_log",
		"ai_conversation", "ai_message", "snippet", "known_host", "terminal_recording", migrationsTable} {
		if !tables[name] {
			t.Errorf("missing table %s", name)
		}
	}
	var count int
	if err := db.DB().QueryRow("SELECT count(*) FROM " + migrationsTable).Scan(&count); err != nil || count != 3 {
		t.Fatalf("migration count=%d err=%v", count, err)
	}
}

func TestFileOpenPragmasAndReopen(t *testing.T) {
	db, path := fileStore(t)
	for query, want := range map[string]string{
		"PRAGMA journal_mode": "wal", "PRAGMA foreign_keys": "1", "PRAGMA synchronous": "1",
	} {
		var got string
		if err := db.DB().QueryRow(query).Scan(&got); err != nil || got != want {
			t.Errorf("%s=%q want %q (err=%v)", query, got, want, err)
		}
	}
	if err := db.SettingSet(context.Background(), "persisted", "yes"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	value, found, err := reopened.SettingGet(context.Background(), "persisted")
	if err != nil || !found || value != "yes" {
		t.Fatalf("value=%q found=%v err=%v", value, found, err)
	}
}

func TestMigrationConcurrentFirstOpen(t *testing.T) {
	for round := 0; round < 20; round++ {
		path := filepath.Join(t.TempDir(), "data.db")
		start := make(chan struct{})
		type openResult struct {
			db  *Store
			err error
		}
		results := make(chan openResult, 2)
		for range 2 {
			go func() {
				<-start
				db, err := Open(context.Background(), path)
				results <- openResult{db: db, err: err}
			}()
		}
		close(start)

		var stores []*Store
		failed := false
		for range 2 {
			result := <-results
			if result.err != nil {
				t.Errorf("round %d concurrent Open: %v", round, result.err)
				failed = true
				continue
			}
			stores = append(stores, result.db)
		}
		for _, db := range stores {
			var count int
			if err := db.DB().QueryRow("SELECT count(*) FROM " + migrationsTable).Scan(&count); err != nil || count != 3 {
				t.Errorf("round %d migration count=%d err=%v", round, count, err)
				failed = true
			}
			_ = db.Close()
		}
		if failed {
			t.FailNow()
		}
	}
}

func TestMigrationRejectsChecksumDrift(t *testing.T) {
	db, path := fileStore(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec("UPDATE "+migrationsTable+" SET checksum=? WHERE version=1", bytes.Repeat([]byte{1}, 32))
	_ = raw.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(context.Background(), path)
	requireCode(t, err, ipc.CodeDBMigrate)
}

func TestMigrationRejectsFutureVersion(t *testing.T) {
	db, path := fileStore(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec("INSERT INTO "+migrationsTable+"(version,description,checksum) VALUES(999,'future',?)", bytes.Repeat([]byte{1}, 32))
	_ = raw.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(context.Background(), path)
	requireCode(t, err, ipc.CodeDBMigrate)
}

func TestForeignKeysAreEnforced(t *testing.T) {
	db := testStore(t)
	_, err := db.AssetCreate(context.Background(), AssetInput{GroupID: ptr("missing"), Kind: "ssh", Name: "bad", OptionsJSON: "{}"})
	requireCode(t, err, ipc.CodeDB)
}

func TestLayoutOptimisticLockingAndCorruptionFallback(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	revision, _, data, err := db.LayoutLoad(ctx)
	if err != nil || revision != 0 || data != nil {
		t.Fatalf("empty layout revision=%d data=%s err=%v", revision, data, err)
	}
	saved, revision, err := db.LayoutSave(ctx, 0, `{"panes":1}`)
	if err != nil || !saved || revision != 1 {
		t.Fatalf("first save saved=%v revision=%d err=%v", saved, revision, err)
	}
	saved, revision, err = db.LayoutSave(ctx, 0, `{"panes":999}`)
	if err != nil || saved || revision != 1 {
		t.Fatalf("stale save saved=%v revision=%d err=%v", saved, revision, err)
	}
	_, _, data, err = db.LayoutLoad(ctx)
	if err != nil || string(data) != `{"panes":1}` {
		t.Fatalf("layout changed after conflict: %s err=%v", data, err)
	}
	_, _, err = db.LayoutSave(ctx, 1, `{not-json`)
	requireCode(t, err, ipc.CodeBadParam)
	if err := db.SettingSet(ctx, LayoutKey, "corrupt"); err != nil {
		t.Fatal(err)
	}
	revision, _, data, err = db.LayoutLoad(ctx)
	if err != nil || revision != 0 || data != nil {
		t.Fatalf("corrupt layout revision=%d data=%s err=%v", revision, data, err)
	}
}

func TestLayoutConcurrentConflict(t *testing.T) {
	ctx := context.Background()
	db, _ := fileStore(t)
	start := make(chan struct{})
	type result struct {
		saved bool
		err   error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			saved, _, err := db.LayoutSave(ctx, 0, `{"writer":`+string(rune('0'+i))+`}`)
			results <- result{saved: saved, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	saves := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.saved {
			saves++
		}
	}
	if saves != 1 {
		t.Fatalf("successful writes=%d, want exactly one", saves)
	}
}
