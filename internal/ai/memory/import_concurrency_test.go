package memory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLegacyImportConcurrentCallsAreSerialized(t *testing.T) {
	target, _ := newMemoryStore(t)
	path := filepath.Join(t.TempDir(), "memory.db")
	writeLegacyDatabase(t, path, []entryRow{legacyDatabaseEntry("concurrent", legacyDatabaseScope, 1, 100, "one import")}, nil)
	start := make(chan struct{})
	type result struct {
		report LegacyImportReport
		err    error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			report, err := target.ImportLegacySidecar(context.Background(), path, legacyDatabaseScope, legacyTargetScope)
			results <- result{report: report, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	inserted := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent import failed: %v", result.err)
		}
		inserted += result.report.EntriesInserted
	}
	if inserted != 1 {
		t.Fatalf("concurrent imports inserted %d rows, want 1", inserted)
	}
	expected := legacyDatabaseEntry("concurrent", legacyTargetScope, 1, 100, "one import")
	if actual := requireMemoryRow(t, target.db, expected.ID); actual != expected {
		t.Fatalf("concurrent imported row = %+v, want %+v", actual, expected)
	}
	archivedBefore, err := os.ReadFile(path + legacyArchiveSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.ImportLegacySidecar(context.Background(), path, legacyDatabaseScope, legacyTargetScope); err != nil {
		t.Fatal(err)
	}
	archivedAfter, err := os.ReadFile(path + legacyArchiveSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(archivedBefore, archivedAfter) {
		t.Fatal("idempotent reentry overwrote the archived database")
	}
}

func TestMoveLegacyFileNoReplaceConcurrentDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "memory.db.migrated")
	sources := []string{filepath.Join(dir, "a"), filepath.Join(dir, "b")}
	for _, source := range sources {
		if err := os.WriteFile(source, []byte(filepath.Base(source)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, len(sources))
	for _, source := range sources {
		go func() {
			<-start
			results <- moveLegacyFileNoReplace(source, destination)
		}()
	}
	close(start)
	var winner int
	for range sources {
		if err := <-results; err == nil {
			winner++
		}
	}
	if winner != 1 {
		t.Fatalf("concurrent moves had %d winners, want exactly 1", winner)
	}
	archived, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	for index, source := range sources {
		content, readErr := os.ReadFile(source)
		if string(archived) == filepath.Base(source) {
			if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("winning source %d was not archived: %v", index, readErr)
			}
			continue
		}
		if readErr != nil || string(content) != filepath.Base(source) {
			t.Fatalf("losing source %d was removed or changed: %q, %v", index, content, readErr)
		}
		if err := moveLegacyFileNoReplace(source, destination); err == nil {
			t.Fatalf("retry overwrote destination with source %d", index)
		}
		unchanged, err := os.ReadFile(destination)
		if err != nil || !bytes.Equal(archived, unchanged) {
			t.Fatalf("destination changed after conflict: %q, %v", unchanged, err)
		}
	}
}

func TestLegacyArchiveCompletesInterruptedHardLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	contents := map[string][]byte{"": []byte("main"), "-wal": []byte("wal"), "-shm": []byte("shm")}
	for suffix, content := range contents {
		if err := os.WriteFile(path+suffix, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(path, path+legacyArchiveSuffix); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveLegacySidecar(path); err != nil {
		t.Fatal(err)
	}
	for suffix, expected := range contents {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("interrupted source %s remains: %v", suffix, err)
		}
		actual, err := os.ReadFile(path + legacyArchiveSuffix + suffix)
		if err != nil || !bytes.Equal(actual, expected) {
			t.Errorf("resumed archive %s = %q, err = %v", suffix, actual, err)
		}
	}
}

func TestLegacyImportReadsQuiescedWALSnapshot(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "live.db")
	legacy, err := Open(context.Background(), sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := legacy.Create(context.Background(), legacyDatabaseScope, CreateInput{Topic: "wal", Content: "entry-from-wal"})
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(sourcePath + "-wal")
	if err != nil || len(wal) == 0 {
		t.Fatalf("legacy fixture has no active WAL: %d bytes, %v", len(wal), err)
	}
	path := filepath.Join(t.TempDir(), "memory.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		content, err := os.ReadFile(sourcePath + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+suffix, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	target, _ := newMemoryStore(t)
	if _, err := target.ImportLegacySidecar(context.Background(), path, legacyDatabaseScope, legacyTargetScope); err != nil {
		t.Fatal(err)
	}
	imported, err := target.Get(context.Background(), legacyTargetScope, entry.ID)
	if err != nil || imported.Content != "entry-from-wal" {
		t.Fatalf("WAL snapshot entry = %+v, err = %v", imported, err)
	}
}
