//go:build windows

package localhelper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageExecutableCopiesByDigestAndDropsOtherBuilds(t *testing.T) {
	stateDir := t.TempDir()
	source := filepath.Join(t.TempDir(), "app.exe")
	if err := os.WriteFile(source, []byte("build-one"), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(stateDir, "bin", "nexterm-helper-0000000000000000.exe")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := stageExecutable(stateDir, source)
	if err != nil {
		t.Fatal(err)
	}
	if first == source || !strings.HasPrefix(filepath.Base(first), "nexterm-helper-") {
		t.Fatalf("staged path = %s", first)
	}
	if data, err := os.ReadFile(first); err != nil || string(data) != "build-one" {
		t.Fatalf("staged copy = %q, %v", data, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("copy of another build was kept: %v", err)
	}
	again, err := stageExecutable(stateDir, source)
	if err != nil || again != first {
		t.Fatalf("restaging the same build = %s, %v; want %s", again, err, first)
	}
	if err := os.WriteFile(first, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := stageExecutable(stateDir, source); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(first); err != nil || string(data) != "build-one" {
		t.Fatalf("a staged copy whose content no longer matches its digest was kept: %q, %v", data, err)
	}
	if err := os.WriteFile(source, []byte("build-two"), 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := stageExecutable(stateDir, source)
	if err != nil || second == first {
		t.Fatalf("a new build must get its own copy: %s, %v", second, err)
	}
}
