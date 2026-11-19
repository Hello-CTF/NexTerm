package localhelper

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBundledHelperIsThePackagedStandaloneHelper(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "NexTerm")
	if _, ok := bundledHelper(executable); ok {
		t.Fatal("found a helper in a directory without one")
	}
	architecture := runtime.GOARCH
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm" {
		architecture = "armv7"
	}
	name := "nexterm-helper-" + runtime.GOOS + "-" + architecture
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	// Helpers of other platforms ship beside it too.
	for _, other := range []string{name, "nexterm-helper-plan9-386"} {
		if err := os.WriteFile(filepath.Join(dir, other), []byte("helper"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := bundledHelper(executable); !ok || got != filepath.Join(dir, name) {
		t.Fatalf("bundledHelper = %q %v, want %s", got, ok, name)
	}
}
