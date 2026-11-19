//go:build unix

package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An older program sharing the state directory starts the newer helper another program put in the store, instead of
// bringing back its own.
func TestOlderProgramStartsTheStoredNewerHelper(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { killHelperProcesses(t, stateDir) })
	if err := PrepareStateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	newer, err := RuntimeMetadataForExecutable(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	newer.Version, newer.Commit = "1.4.5", "c-new"
	stored, err := InstallHelper(stateDir, os.Args[0], newer)
	if err != nil {
		t.Fatal(err)
	}

	older := release("1.4.3")
	path, launched, err := HelperExecutable(stateDir, storeSource(t, "an older program"), older)
	if err != nil || path != stored.Path || launched.Version != "1.4.5" {
		t.Fatalf("older program got %q %+v %v, want the stored %s", path, launched, err, stored.Path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	config := HelperConfig{StateDir: stateDir, Executable: path, Release: &launched, SpawnTimeout: 30 * time.Second, SpawnedBy: SpawnedByAgent}
	helper, err := ConnectHelper(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if !helper.Spawned() {
		t.Fatal("no helper was started")
	}
	running, err := InspectHelper(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if running.Runtime == nil || running.Runtime.SHA256 != stored.Release.SHA256 {
		t.Fatalf("running helper = %+v, want the stored executable %s", running.Runtime, stored.Release.SHA256)
	}
	// Judged against the stored helper, the running one is current.
	build, err := EnsureHelperBuild(ctx, config)
	if err != nil || build.Stale || build.Replaced {
		t.Fatalf("EnsureHelperBuild = %+v %v, want the stored helper to be current", build, err)
	}
}
