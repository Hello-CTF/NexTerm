package update

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func requireStageEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Fatalf("stage directory keeps %s", entry.Name())
	}
}

type stageFixture struct {
	executable string
	stageDir   string
	restarts   int
	manager    *Manager
}

func newStageFixture(t *testing.T, version, stageDir string) *stageFixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("linux deb install test")
	}
	fixture := linuxFixture(t, version, false)
	server := fixture.server(t)
	executable := filepath.Join(t.TempDir(), "nexterm-desktop")
	if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	stage := &stageFixture{executable: executable, stageDir: stageDir}
	stage.manager = &Manager{
		CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true, StageDir: stageDir,
		ExePath: executable, APIURL: server.URL + "/releases",
		RestartFunc: func(context.Context, string) error {
			stage.restarts++
			return nil
		},
	}
	return stage
}

func (s *stageFixture) content(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(s.executable)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRestartRefusesTamperedStagedPackage(t *testing.T) {
	stage := newStageFixture(t, "0.3.0", t.TempDir())
	if result, err := stage.manager.Install(context.Background(), ""); err != nil || !result.Ready {
		t.Fatalf("install = %+v, %v", result, err)
	}
	staged, ok := stage.manager.stagedPackage()
	if !ok {
		t.Fatal("install must stage the package")
	}
	if err := os.WriteFile(staged.path(stage.stageDir), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := stage.manager.Restart(context.Background())
	if result.Installed || result.Restarted || !strings.Contains(result.UnavailableReason, "校验失败") {
		t.Fatalf("restart = %+v, want the changed package refused", result)
	}
	if stage.restarts != 0 || stage.content(t) != "old-binary" {
		t.Fatalf("restarts = %d, content = %q; nothing may be applied or restarted", stage.restarts, stage.content(t))
	}
	if state := stage.manager.State(); state.Phase != StateFailed || state.ReadyVersion != "" {
		t.Fatalf("state = %+v", state)
	}
	requireStageEmpty(t, stage.stageDir)
}

func TestStagedPackageIsOfferedAgainAfterRelaunch(t *testing.T) {
	stageDir := t.TempDir()
	first := newStageFixture(t, "0.3.0", stageDir)
	if result, err := first.manager.Install(context.Background(), ""); err != nil || !result.Ready {
		t.Fatalf("install = %+v, %v", result, err)
	}
	// The app exits without restarting; the next launch finds the package ready and applies it on restart.
	next := newStageFixture(t, "0.3.0", stageDir)
	if state := next.manager.State(); state.Phase != StateReady || state.ReadyVersion != "0.3.0" {
		t.Fatalf("state after relaunch = %+v", state)
	}
	result := next.manager.Restart(context.Background())
	if !result.Installed || !result.Restarted || next.content(t) != "new-binary-0.3.0" {
		t.Fatalf("restart = %+v, content = %q", result, next.content(t))
	}
	requireStageEmpty(t, stageDir)
}

func TestStaleStagedPackageIsDiscarded(t *testing.T) {
	stageDir := t.TempDir()
	first := newStageFixture(t, "0.3.0", stageDir)
	if result, err := first.manager.Install(context.Background(), ""); err != nil || !result.Ready {
		t.Fatalf("install = %+v, %v", result, err)
	}
	// The next launch already runs the staged version (updated some other way).
	next := newStageFixture(t, "0.3.0", stageDir)
	next.manager.CurrentVersion = "0.3.0"
	if state := next.manager.State(); state.Phase != StateIdle || state.ReadyVersion != "" {
		t.Fatalf("state = %+v, want the stale package dropped", state)
	}
	requireStageEmpty(t, stageDir)

	// A stale package that appears after the launch is refused by the restart itself.
	staged := newStageFixture(t, "0.3.0", stageDir)
	if result, err := staged.manager.Install(context.Background(), ""); err != nil || !result.Ready {
		t.Fatalf("install = %+v, %v", result, err)
	}
	next.manager.CurrentVersion = "0.3.0"
	result := next.manager.Restart(context.Background())
	if result.Installed || !strings.Contains(result.UnavailableReason, "不比当前版本新") || next.content(t) != "old-binary" {
		t.Fatalf("restart = %+v, content = %q", result, next.content(t))
	}
	requireStageEmpty(t, stageDir)
}

func TestNewerReleaseReplacesStagedPackage(t *testing.T) {
	stageDir := t.TempDir()
	older := newStageFixture(t, "0.3.0", stageDir)
	if result, err := older.manager.Install(context.Background(), ""); err != nil || !result.Ready {
		t.Fatalf("install = %+v, %v", result, err)
	}
	newer := newStageFixture(t, "0.4.0", stageDir)
	if result, err := newer.manager.Install(context.Background(), ""); err != nil || !result.Ready || result.Version != "0.4.0" {
		t.Fatalf("install = %+v, %v", result, err)
	}
	staged, ok := newer.manager.stagedPackage()
	if !ok || staged.Version != "0.4.0" {
		t.Fatalf("staged = %+v", staged)
	}
	entries, err := os.ReadDir(filepath.Join(stageDir, stagedPackageDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != staged.AssetName {
		t.Fatalf("package directory = %v, want only the newer package", entries)
	}
}

func TestStagedManifestRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../outside.deb", "dir/file.deb"} {
		if (stagedPackage{Version: "0.3.0", AssetName: name, SHA256: strings.Repeat("0", 64)}).valid() {
			t.Fatalf("asset name %q must be rejected", name)
		}
	}
}
