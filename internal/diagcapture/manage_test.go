package diagcapture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func makeCapture(t *testing.T, root, name string, withZip bool) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if withZip {
		if err := os.WriteFile(dir+".zip", []byte("zip"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestDeleteRemovesFolderAndArchiveOnly(t *testing.T) {
	paths, _ := newEnv(t)
	root := Root(paths)
	keep := makeCapture(t, root, "20260101T000000Z", true)
	gone := makeCapture(t, root, "20260102T000000Z", true)
	if err := Delete(paths, gone, ""); err != nil {
		t.Fatal(err)
	}
	if exists(gone) || exists(gone+".zip") {
		t.Fatal("folder or archive survived Delete")
	}
	if !exists(keep) || !exists(keep+".zip") {
		t.Fatal("Delete touched another capture")
	}
}

func TestDeleteRefusesTraversalSymlinksAndTheActiveCapture(t *testing.T) {
	paths, _ := newEnv(t)
	root := Root(paths)
	good := makeCapture(t, root, "20260101T000000Z", false)
	outside := filepath.Join(paths.DataDir, "secret")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"", ".", "..", root, outside, "../secret", filepath.Join(good, "..", "..", "secret"), filepath.Join(root, "missing")} {
		if err := Delete(paths, dir, ""); err == nil {
			t.Errorf("Delete(%q) should be rejected", dir)
		}
	}
	if !exists(outside) || !exists(good) {
		t.Fatal("a rejected Delete removed something")
	}
	if err := Delete(paths, good, good); !errors.Is(err, ErrCaptureActive) {
		t.Fatalf("deleting the active capture = %v, want ErrCaptureActive", err)
	}
	if !exists(good) {
		t.Fatal("the active capture was removed")
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "20260103T000000Z")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if err := Delete(paths, link, ""); err == nil {
			t.Fatal("Delete followed a symlink")
		}
		if !exists(outside) {
			t.Fatal("the symlink target was removed")
		}
	}
}

func TestClearKeepsTheActiveCaptureAndForeignEntries(t *testing.T) {
	paths, _ := newEnv(t)
	root := Root(paths)
	active := makeCapture(t, root, "20260101T000000Z", true)
	makeCapture(t, root, "20260102T000000Z", true)
	makeCapture(t, root, "20260103T000000Z", false)
	stray := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(stray, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := Clear(paths, active)
	if err != nil || removed != 2 {
		t.Fatalf("Clear = %d, %v; want 2 removed", removed, err)
	}
	if !exists(active) || !exists(active+".zip") || !exists(stray) {
		t.Fatal("Clear removed the active capture or a foreign file")
	}
	entries, _ := List(paths)
	if len(entries) != 1 {
		t.Fatalf("captures left = %d, want 1", len(entries))
	}
}

func TestAppendFrontendNeedsAnActiveCaptureAndHonoursTheCap(t *testing.T) {
	if err := AppendFrontend([]byte(`{}`)); !errors.Is(err, ErrNoActiveCapture) {
		t.Fatalf("no capture: %v", err)
	}
	paths, logger := newEnv(t)
	s, err := Start(context.Background(), paths, logger, Info{Mode: "test", Source: "flag"}, fastOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	if err := AppendFrontend([]byte(`{"kind":"start"}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir(), FrontendFile))
	if err != nil || strings.TrimSpace(string(data)) != `{"kind":"start"}` {
		t.Fatalf("frontend file = %q, %v", data, err)
	}
	info, _ := os.Stat(filepath.Join(s.Dir(), FrontendFile))
	if info.Mode().Perm() != 0o600 && runtime.GOOS != "windows" {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	big := []byte(strings.Repeat("a", 1<<20))
	var full error
	for i := 0; i < 8 && full == nil; i++ {
		full = AppendFrontend(big)
	}
	if !errors.Is(full, ErrFrontendFull) {
		t.Fatalf("expected the cap to stop writes, got %v", full)
	}
}
