package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func storeSource(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func release(version string) RuntimeMetadata {
	return RuntimeMetadata{Version: version, Commit: "c-" + version}
}

func installOrFail(t *testing.T, stateDir, source string, metadata RuntimeMetadata) StoredHelper {
	t.Helper()
	stored, err := InstallHelper(stateDir, source, metadata)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func storedContent(t *testing.T, stored StoredHelper) string {
	t.Helper()
	content, err := os.ReadFile(stored.Path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestHelperStoreKeepsTheNewestRelease(t *testing.T) {
	stateDir := t.TempDir()
	stored := installOrFail(t, stateDir, storeSource(t, "v1.4.3"), release("1.4.3"))
	if storedContent(t, stored) != "v1.4.3" || stored.Release.Version != "1.4.3" || stored.Release.Protocol != ProtocolVersion {
		t.Fatalf("first install = %+v", stored)
	}
	if info, err := os.Stat(stored.Path); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("stored helper is not executable: %v %v", info, err)
	}
	stored = installOrFail(t, stateDir, storeSource(t, "v1.4.5"), release("1.4.5"))
	if storedContent(t, stored) != "v1.4.5" {
		t.Fatalf("a later release did not replace the stored helper: %+v", stored)
	}
	// An older program and another build of the same release keep the newest helper.
	for _, offer := range []struct{ content, version string }{{"v1.4.4", "1.4.4"}, {"v1.4.5-agent", "1.4.5"}} {
		metadata := release(offer.version)
		metadata.Commit = "c-1.4.5"
		if stored = installOrFail(t, stateDir, storeSource(t, offer.content), metadata); storedContent(t, stored) != "v1.4.5" {
			t.Fatalf("offer %s replaced the newest helper: %q", offer.content, storedContent(t, stored))
		}
	}
	if current, ok := CurrentStoredHelper(stateDir); !ok || current.Path != stored.Path {
		t.Fatalf("current = %+v %v, want %s", current, ok, stored.Path)
	}
}

func TestHelperStoreDevBuilds(t *testing.T) {
	stateDir := t.TempDir()
	installOrFail(t, stateDir, storeSource(t, "release"), release("1.4.5"))
	dev := RuntimeMetadata{Version: "dev"}
	if stored := installOrFail(t, stateDir, storeSource(t, "dev-a"), dev); storedContent(t, stored) != "dev-a" {
		t.Fatal("a dev build did not take over the store")
	}
	if stored := installOrFail(t, stateDir, storeSource(t, "release-2"), release("1.5.0")); storedContent(t, stored) != "dev-a" {
		t.Fatal("a release replaced a dev build, so the two would swap back and forth")
	}
	if stored := installOrFail(t, stateDir, storeSource(t, "dev-b"), dev); storedContent(t, stored) != "dev-b" {
		t.Fatal("a rebuilt dev build did not replace the previous one")
	}
}

func TestHelperStoreReplacesADamagedHelperAndKeepsThePrevious(t *testing.T) {
	stateDir := t.TempDir()
	first := installOrFail(t, stateDir, storeSource(t, "one"), release("1.0.0"))
	second := installOrFail(t, stateDir, storeSource(t, "two"), release("2.0.0"))
	third := installOrFail(t, stateDir, storeSource(t, "three"), release("3.0.0"))
	// The previous helper stays for a replacement that has to start it again; older ones go.
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatalf("previous helper removed: %v", err)
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Fatalf("an older helper stayed: %v", err)
	}
	if err := os.WriteFile(third.Path, []byte("thr"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := CurrentStoredHelper(stateDir); ok {
		t.Fatal("a truncated helper is still used")
	}
	if stored := installOrFail(t, stateDir, storeSource(t, "older"), release("0.9.0")); storedContent(t, stored) != "older" {
		t.Fatal("a damaged store was not repaired by the next offer")
	}
	entries, _ := os.ReadDir(filepath.Join(stateDir, helperStoreDir))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), helperStoreStaging) {
			t.Fatalf("staging file left: %s", entry.Name())
		}
	}
}

func TestHelperExecutableFallsBackToTheSource(t *testing.T) {
	stateDir := t.TempDir()
	source := storeSource(t, "content")
	// A digest that does not match the file (it changed while this program ran) is not recorded.
	metadata := release("1.4.5")
	metadata.SHA256 = strings.Repeat("ab", 32)
	path, got, err := HelperExecutable(stateDir, source, metadata)
	if err == nil || path != source || got.SHA256 != metadata.SHA256 {
		t.Fatalf("HelperExecutable = %q %+v %v, want the source and an error", path, got, err)
	}
	if _, ok := CurrentStoredHelper(stateDir); ok {
		t.Fatal("a mismatching copy was recorded")
	}
}

func TestHelperStoreConcurrentInstallsKeepTheNewest(t *testing.T) {
	stateDir := t.TempDir()
	versions := []string{"1.0.0", "1.2.0", "1.1.0", "1.3.0", "0.9.0"}
	sources := make([]string, len(versions))
	for i, version := range versions {
		sources[i] = storeSource(t, "helper "+version)
	}
	errs := make(chan error, len(versions))
	for i, version := range versions {
		go func() {
			_, err := InstallHelper(stateDir, sources[i], release(version))
			errs <- err
		}()
	}
	for range versions {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	current, ok := CurrentStoredHelper(stateDir)
	if !ok || current.Release.Version != "1.3.0" || storedContent(t, current) != "helper 1.3.0" {
		t.Fatalf("store after concurrent installs = %+v %v, want 1.3.0", current, ok)
	}
}
