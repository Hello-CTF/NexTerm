package local

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func startWatch(t *testing.T, dirs ...string) (base.WatchHandle, <-chan base.WatchEvent, <-chan error) {
	t.Helper()
	events := make(chan base.WatchEvent, 64)
	done := make(chan error, 1)
	handle, err := New().Watch(context.Background(), func(event base.WatchEvent) { events <- event }, func(err error) { done <- err })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := handle.Set(dirs); err != nil {
		t.Fatal(err)
	}
	return handle, events, done
}

func nextEvent(t *testing.T, events <-chan base.WatchEvent) base.WatchEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("no watch event")
		return base.WatchEvent{}
	}
}

func TestWatchBatchesChangesPerDirectory(t *testing.T) {
	dir := t.TempDir()
	_, events, _ := startWatch(t, dir)
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]bool{}
	for !got["a.txt"] || !got["b.txt"] {
		event := nextEvent(t, events)
		if event.Dir != dir || event.Unwatchable {
			t.Fatalf("event = %+v", event)
		}
		for _, name := range event.Names {
			got[name] = true
		}
	}
}

// Editors and tools save by writing a temporary file and renaming it over the
// original; the watch follows the directory, so the replaced name is reported.
func TestWatchReportsRenameOverExistingFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, events, _ := startWatch(t, dir)
	temporary := filepath.Join(dir, ".config.yml.tmp")
	if err := os.WriteFile(temporary, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, target); err != nil {
		t.Fatal(err)
	}
	for {
		event := nextEvent(t, events)
		if slices.Contains(event.Names, "config.yml") {
			return
		}
	}
}

func TestWatchSetReplacesDirectoriesAndReportsUnwatchable(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	handle, events, _ := startWatch(t, first)
	missing := filepath.Join(second, "missing")
	if err := handle.Set([]string{second, missing}); err != nil {
		t.Fatal(err)
	}
	event := nextEvent(t, events)
	if event.Dir != missing || !event.Unwatchable || event.Reason == "" {
		t.Fatalf("event for a missing directory = %+v, want it unwatchable", event)
	}
	if err := os.WriteFile(filepath.Join(first, "ignored"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "seen"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		event := nextEvent(t, events)
		if event.Dir == first {
			t.Fatalf("event from a directory no longer watched: %+v", event)
		}
		if event.Dir == second && slices.Contains(event.Names, "seen") {
			return
		}
	}
}

func TestWatchCloseEndsCleanly(t *testing.T) {
	handle, _, done := startWatch(t, t.TempDir())
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("done after Close = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not end after Close")
	}
	if err := handle.Set(nil); err == nil {
		t.Fatal("Set after Close succeeded")
	}
}
