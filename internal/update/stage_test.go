package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/releasesig/releasesigtest"
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

type stageOptions struct {
	key        *releasesigtest.Key
	prerelease bool
	channel    *ReleaseChannel
}

func newStageFixture(t *testing.T, version, stageDir string) *stageFixture {
	t.Helper()
	return newStageFixtureWith(t, version, stageDir, stageOptions{})
}

func newStageFixtureWith(t *testing.T, version, stageDir string, options stageOptions) *stageFixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("linux deb install test")
	}
	fixture := linuxFixture(t, version, false)
	fixture.releases[0].Prerelease = options.prerelease
	if options.key != nil {
		fixture.sign(*options.key, version)
	}
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
	if options.key != nil {
		stage.manager.MinisignPublicKey = options.key.PublicKeyFile()
		stage.manager.RequireSignedChecksums = true
	}
	if options.channel != nil {
		stage.manager.ChannelProvider = func(context.Context) ReleaseChannel { return *options.channel }
	}
	return stage
}

func (s *stageFixture) install(t *testing.T) stagedPackage {
	t.Helper()
	if result, err := s.manager.Install(context.Background(), ""); err != nil || !result.Ready {
		t.Fatalf("install = %+v, %v", result, err)
	}
	staged, ok := s.manager.stagedPackage()
	if !ok {
		t.Fatal("install must stage the package")
	}
	return staged
}

// requireRefused restarts and expects the staged package refused: nothing applied, no relaunch, stage removed.
func (s *stageFixture) requireRefused(t *testing.T, manager *Manager) Result {
	t.Helper()
	result := manager.Restart(context.Background())
	if result.Installed || result.Restarted || result.Failure != FailureApply || result.UnavailableReason == "" {
		t.Fatalf("restart = %+v, want the staged package refused", result)
	}
	if s.restarts != 0 || s.content(t) != "old-binary" {
		t.Fatalf("restarts = %d, content = %q; nothing may be applied or relaunched", s.restarts, s.content(t))
	}
	if staged, ok := manager.stagedPackage(); ok {
		t.Fatalf("refused package %+v must be discarded", staged)
	}
	return result
}

func sumsLine(name string, data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]) + "  " + name + "\n"
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
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, ",") != checksumsAssetName+","+staged.AssetName {
		t.Fatalf("package directory = %v, want only the newer package and its checksums", names)
	}
}

func TestStagedPointerRejectsOtherAssetNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../outside.deb", "dir/nexterm-desktop_linux_amd64_0.3.0.deb",
		"nexterm-desktop_linux_amd64_0.4.0.deb", "nexterm-desktop_macos_amd64_0.3.0.dmg", "evil.deb"} {
		if (stagedPackage{Version: "0.3.0", AssetName: name}).validFor("linux", "amd64") {
			t.Fatalf("asset name %q must be rejected", name)
		}
	}
	if !(stagedPackage{Version: "0.3.0", AssetName: "nexterm-desktop_linux_amd64_0.3.0.deb"}).validFor("linux", "amd64") {
		t.Fatal("the release's own asset name must be accepted")
	}
}

// staged.json is only a pointer: a package swapped together with a forged, unsigned SHA256SUMS is refused because
// the manifest's signature no longer verifies.
func TestRestartRefusesPackageWithForgedChecksums(t *testing.T) {
	key := releasesigtest.NewKey(31)
	stage := newStageFixtureWith(t, "0.3.0", t.TempDir(), stageOptions{key: &key})
	staged := stage.install(t)
	evil := []byte("evil package")
	if err := os.WriteFile(staged.path(stage.stageDir), evil, 0o600); err != nil {
		t.Fatal(err)
	}
	sumsPath := filepath.Join(stage.stageDir, stagedPackageDir, checksumsAssetName)
	if err := os.WriteFile(sumsPath, []byte(sumsLine(staged.AssetName, evil)), 0o600); err != nil {
		t.Fatal(err)
	}
	result := stage.requireRefused(t, stage.manager)
	if !strings.Contains(result.UnavailableReason, "校验失败") {
		t.Fatalf("reason = %q", result.UnavailableReason)
	}
}

// A signed manifest of one version cannot vouch for another: bumping the pointer's version (and renaming the
// package to match) breaks the signature's trusted comment.
func TestRestartRefusesBumpedStagedVersion(t *testing.T) {
	key := releasesigtest.NewKey(32)
	stage := newStageFixtureWith(t, "0.3.0", t.TempDir(), stageOptions{key: &key})
	staged := stage.install(t)
	bumped := stagedPackage{Version: "0.9.0", AssetName: "nexterm-desktop_linux_amd64_0.9.0.deb"}
	if err := os.Rename(staged.path(stage.stageDir), bumped.path(stage.stageDir)); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(bumped)
	if err := os.WriteFile(filepath.Join(stage.stageDir, stagedManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	stage.requireRefused(t, stage.manager)
}

func TestRestartRefusesSwappedSignature(t *testing.T) {
	key := releasesigtest.NewKey(33)
	stage := newStageFixtureWith(t, "0.3.0", t.TempDir(), stageOptions{key: &key})
	stage.install(t)
	other := releasesigtest.NewKey(34)
	signature := other.Sign([]byte("anything"), "NexTerm 0.3.0 SHA256SUMS")
	if err := os.WriteFile(filepath.Join(stage.stageDir, stagedPackageDir, checksumSignatureAssetName), []byte(signature), 0o600); err != nil {
		t.Fatal(err)
	}
	stage.requireRefused(t, stage.manager)
}

func TestSignedStagedPackageAppliesAfterRelaunch(t *testing.T) {
	key := releasesigtest.NewKey(35)
	stageDir := t.TempDir()
	first := newStageFixtureWith(t, "0.3.0", stageDir, stageOptions{key: &key})
	first.install(t)
	next := newStageFixtureWith(t, "0.3.0", stageDir, stageOptions{key: &key})
	if state := next.manager.State(); state.Phase != StateReady || state.ReadyVersion != "0.3.0" {
		t.Fatalf("state = %+v", state)
	}
	if result := next.manager.Restart(context.Background()); !result.Installed || !result.Restarted || next.content(t) != "new-binary-0.3.0" {
		t.Fatalf("restart = %+v, content = %q", result, next.content(t))
	}
	requireStageEmpty(t, stageDir)
}

func TestRestartRefusesSymlinkedPackage(t *testing.T) {
	stage := newStageFixture(t, "0.3.0", t.TempDir())
	staged := stage.install(t)
	path := staged.path(stage.stageDir)
	elsewhere := filepath.Join(t.TempDir(), "real.deb")
	if err := os.Rename(path, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, path); err != nil {
		t.Fatal(err)
	}
	stage.requireRefused(t, stage.manager)
}

func TestStagedReleaseCandidateIsDroppedOnStableChannel(t *testing.T) {
	channel := ChannelRC
	stage := newStageFixtureWith(t, "0.3.0-rc.1", t.TempDir(), stageOptions{prerelease: true, channel: &channel})
	stage.install(t)
	if state := stage.manager.State(); state.ReadyVersion != "0.3.0-rc.1" {
		t.Fatalf("state = %+v", state)
	}
	channel = ChannelStable
	if state := stage.manager.State(); state.ReadyVersion != "" || state.Phase == StateReady {
		t.Fatalf("state after switching to stable = %+v, want the candidate dropped", state)
	}
	requireStageEmpty(t, stage.stageDir)

	// A candidate staged again is refused by the restart itself once stable is selected.
	channel = ChannelRC
	stage.install(t)
	channel = ChannelStable
	stage.requireRefused(t, stage.manager)
}

func TestRestartRefusesToRelaunchWhenTheOfferedPackageIsGone(t *testing.T) {
	stage := newStageFixture(t, "0.3.0", t.TempDir())
	stage.install(t)
	if err := removeStaged(stage.stageDir); err != nil {
		t.Fatal(err)
	}
	result := stage.manager.Restart(context.Background())
	if result.Restarted || result.Failure != FailureApply || stage.restarts != 0 {
		t.Fatalf("restart = %+v, restarts = %d; a missing package must not look like a finished update", result, stage.restarts)
	}
	if state := stage.manager.State(); state.ReadyVersion != "" {
		t.Fatalf("state = %+v", state)
	}
}

func TestRestartAppliesDespiteCancelledCaller(t *testing.T) {
	stage := newStageFixture(t, "0.3.0", t.TempDir())
	stage.install(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := stage.manager.Restart(ctx); !result.Installed || !result.Restarted || stage.content(t) != "new-binary-0.3.0" {
		t.Fatalf("restart = %+v, content = %q", result, stage.content(t))
	}
}

func TestRelaunchFailureAfterApplyIsReportedApart(t *testing.T) {
	stage := newStageFixture(t, "0.3.0", t.TempDir())
	stage.install(t)
	stage.manager.RestartFunc = func(context.Context, string) error { return errors.New("spawn failed") }
	result := stage.manager.Restart(context.Background())
	if !result.Installed || result.Restarted || result.Failure != FailureRelaunch || stage.content(t) != "new-binary-0.3.0" {
		t.Fatalf("restart = %+v, content = %q", result, stage.content(t))
	}
	if state := stage.manager.State(); state.Phase != StateInstalled {
		t.Fatalf("state = %+v", state)
	}
}

func TestLoadStagedSweepsLeftoversAndInstallTightensTheDirectory(t *testing.T) {
	stageDir := filepath.Join(t.TempDir(), "update")
	if err := os.MkdirAll(filepath.Join(stageDir, ".download-123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, ".staged-456"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stage := newStageFixture(t, "0.3.0", stageDir)
	if state := stage.manager.State(); state.Phase != StateIdle {
		t.Fatalf("state = %+v", state)
	}
	requireStageEmpty(t, stageDir)
	stage.install(t)
	info, err := os.Stat(stageDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("stage directory mode = %v, want 0700", info.Mode().Perm())
	}
}
