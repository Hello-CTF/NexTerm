package update

import (
	"context"
	"github.com/Hello-CTF/NexTerm/internal/releasesig/releasesigtest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckSignedRelease(t *testing.T) {
	key := releasesigtest.NewKey(10)
	fixture := linuxFixture(t, "0.3.0", false)
	fixture.sign(key, "0.3.0")
	server := fixture.server(t)
	manager := &Manager{
		CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true, StageDir: t.TempDir(),
		APIURL: server.URL + "/releases", MinisignPublicKey: key.PublicKeyFile(), RequireSignedChecksums: true,
	}
	status := manager.Check(context.Background())
	if !status.Available || status.Version != "0.3.0" || status.AssetName != fixture.archiveName || status.UnavailableReason != "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestInstallSignatureFailuresKeepOldBinary(t *testing.T) {
	tests := []struct {
		name          string
		noEmbeddedKey bool
		setup         func(*releaseFixture, releasesigtest.Key, *Manager)
	}{
		{
			name: "missing signature asset",
			setup: func(fixture *releaseFixture, key releasesigtest.Key, manager *Manager) {
				fixture.unsign()
				manager.MinisignPublicKey = key.PublicKeyFile()
				manager.RequireSignedChecksums = true
			},
		},
		{
			name: "signature download missing",
			setup: func(fixture *releaseFixture, key releasesigtest.Key, manager *Manager) {
				fixture.signature = ""
				manager.MinisignPublicKey = key.PublicKeyFile()
			},
		},
		{
			name: "wrong trusted key",
			setup: func(fixture *releaseFixture, key releasesigtest.Key, manager *Manager) {
				fixture.sign(key, "0.3.0")
				manager.MinisignPublicKey = releasesigtest.NewKey(99).PublicKeyFile()
			},
		},
		{
			name: "signature for another manifest",
			setup: func(fixture *releaseFixture, key releasesigtest.Key, manager *Manager) {
				fixture.sign(key, "0.3.0")
				fixture.signature = key.Sign([]byte("another manifest"), "NexTerm 0.3.0 SHA256SUMS")
				manager.MinisignPublicKey = key.PublicKeyFile()
			},
		},
		{
			name: "signature for another version",
			setup: func(fixture *releaseFixture, key releasesigtest.Key, manager *Manager) {
				fixture.sign(key, "0.2.9")
				manager.MinisignPublicKey = key.PublicKeyFile()
			},
		},
		{
			name: "malformed signature",
			setup: func(fixture *releaseFixture, key releasesigtest.Key, manager *Manager) {
				fixture.sign(key, "0.3.0")
				fixture.signature = "not a minisign signature"
				manager.MinisignPublicKey = key.PublicKeyFile()
			},
		},
		{
			name:          "required without trusted key",
			noEmbeddedKey: true,
			setup: func(_ *releaseFixture, _ releasesigtest.Key, manager *Manager) {
				manager.RequireSignedChecksums = true
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.noEmbeddedKey {
				withoutEmbeddedKey(t)
			}
			key := releasesigtest.NewKey(10)
			fixture := linuxFixture(t, "0.3.0", false)
			executable := filepath.Join(t.TempDir(), "nexterm-desktop")
			if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			manager := &Manager{
				CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true, StageDir: t.TempDir(), ExePath: executable,
			}
			test.setup(fixture, key, manager)
			server := fixture.server(t)
			manager.APIURL = server.URL + "/releases"

			status := manager.Check(context.Background())
			if status.Available || status.UnavailableReason == "" {
				t.Fatalf("status = %+v", status)
			}
			result, err := manager.Install(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			if result.Installed || result.UnavailableReason == "" {
				t.Fatalf("result = %+v", result)
			}
			content, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != "old-binary" {
				t.Fatalf("executable must not be replaced, content = %q", content)
			}
		})
	}
}

func TestInstallSignedChecksumMismatchKeepsOldBinary(t *testing.T) {
	key := releasesigtest.NewKey(10)
	fixture := linuxFixture(t, "0.3.0", true)
	fixture.sign(key, "0.3.0")
	server := fixture.server(t)
	executable := filepath.Join(t.TempDir(), "nexterm-desktop")
	if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true, StageDir: t.TempDir(),
		ExePath: executable, APIURL: server.URL + "/releases", MinisignPublicKey: key.PublicKeyFile(),
	}
	if _, err := manager.Install(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("install error = %v", err)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "old-binary" {
		t.Fatalf("executable must not be replaced, content = %q", content)
	}
}

func TestInstallSignedReleaseReplacesBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux deb install test")
	}
	key := releasesigtest.NewKey(10)
	fixture := linuxFixture(t, "0.3.0", false)
	fixture.sign(key, "0.3.0")
	server := fixture.server(t)
	executable := filepath.Join(t.TempDir(), "nexterm-desktop")
	if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true, StageDir: t.TempDir(),
		ExePath: executable, APIURL: server.URL + "/releases", MinisignPublicKey: key.PublicKeyFile(), RequireSignedChecksums: true,
		RestartFunc: func(context.Context, string) error { return nil },
	}
	result, err := manager.Install(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.Installed || result.Version != "0.3.0" {
		t.Fatalf("result = %+v", result)
	}
	if restart := manager.Restart(context.Background()); !restart.Installed || !restart.Restarted {
		t.Fatalf("restart = %+v", restart)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new-binary-0.3.0" {
		t.Fatalf("executable content = %q", content)
	}
}

// A build without a trusted key (a development build: release builds always embed one) still reports a newer
// release and its download link, but installs nothing, and never applies a package staged without a signature.
func TestBuildWithoutTrustedKeyInstallsNothing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux deb install test")
	}
	stage := newStageFixture(t, "0.3.0", t.TempDir())
	staged := stage.install(t)
	withoutEmbeddedKey(t)

	status := stage.manager.Check(context.Background())
	if !status.Available || status.Version != "0.3.0" || status.DownloadURL == "" {
		t.Fatalf("status = %+v, want the newer release reported", status)
	}
	if status.CanInstall || status.UnavailableReason == "" {
		t.Fatalf("status = %+v, want installing refused without a trusted key", status)
	}
	result, err := stage.manager.Install(context.Background(), "")
	if err != nil || result.Ready || result.UnavailableReason == "" {
		t.Fatalf("install = %+v, %v; want it refused without a trusted key", result, err)
	}

	// The package staged while the key was trusted is neither offered nor applied once the build has none.
	relaunched := &Manager{
		CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true, StageDir: stage.stageDir,
		ExePath: stage.executable, RestartFunc: func(context.Context, string) error { return nil },
	}
	if state := relaunched.State(); state.Phase == StateReady || state.ReadyVersion != "" {
		t.Fatalf("state = %+v, package %+v must not be offered without a trusted key", state, staged)
	}
	if result := relaunched.Restart(context.Background()); result.Installed {
		t.Fatalf("restart = %+v, want nothing installed", result)
	}
	if content := stage.content(t); content != "old-binary" {
		t.Fatalf("executable content = %q, want it untouched", content)
	}
	if staged, ok := relaunched.stagedPackage(); ok {
		t.Fatalf("unverifiable package %+v must be discarded", staged)
	}
}
