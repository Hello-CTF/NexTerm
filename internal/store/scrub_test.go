package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func requireFileTokenAbsent(t *testing.T, path, token string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte(token)) {
		t.Fatalf("%s still contains remnant token %q", path, token)
	}
}

func requireFileTokenPresent(t *testing.T, path, token string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte(token)) {
		t.Fatalf("%s does not contain expected token %q", path, token)
	}
}

func TestScrubFreeSpaceRemovesRemnantBytes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.SettingSet(ctx, "scrub.key", "remnant-token-abc"); err != nil {
		t.Fatal(err)
	}
	requireFileTokenPresent(t, path+"-wal", "remnant-token-abc")
	if err := database.SettingSet(ctx, "scrub.key", "replacement"); err != nil {
		t.Fatal(err)
	}
	if err := database.ScrubFreeSpace(ctx); err != nil {
		t.Fatal(err)
	}
	if got, found, err := database.SettingGet(ctx, "scrub.key"); err != nil || !found || got != "replacement" {
		t.Fatalf("scrub damaged live data: %q found=%v err=%v", got, found, err)
	}
	requireFileTokenAbsent(t, path, "remnant-token-abc")
	requireFileTokenAbsent(t, path+"-wal", "remnant-token-abc")
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	requireFileTokenAbsent(t, path, "remnant-token-abc")
}

func TestScrubFreeSpaceOnMemoryStore(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	if err := database.SettingSet(ctx, "scrub.key", "value"); err != nil {
		t.Fatal(err)
	}
	if err := database.ScrubFreeSpace(ctx); err != nil {
		t.Fatal(err)
	}
	if got, found, err := database.SettingGet(ctx, "scrub.key"); err != nil || !found || got != "value" {
		t.Fatalf("scrub damaged live data: %q found=%v err=%v", got, found, err)
	}
}
