package update

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckSignedRelease(t *testing.T) {
	key := newTestMinisignKey(10)
	fixture := linuxFixture(t, "0.3.0", false)
	fixture.sign(key, "0.3.0")
	server := fixture.server(t)
	manager := &Manager{
		CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true,
		APIURL: server.URL + "/releases", MinisignPublicKey: key.publicKeyFile(), RequireSignedChecksums: true,
	}
	status := manager.Check(context.Background())
	if !status.Available || status.Version != "0.3.0" || status.AssetName != fixture.archiveName || status.UnavailableReason != "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestInstallSignatureFailuresKeepOldBinary(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*releaseFixture, testMinisignKey, *Manager)
	}{
		{
			name: "missing signature asset",
			setup: func(_ *releaseFixture, key testMinisignKey, manager *Manager) {
				manager.MinisignPublicKey = key.publicKeyFile()
				manager.RequireSignedChecksums = true
			},
		},
		{
			name: "signature download missing",
			setup: func(fixture *releaseFixture, key testMinisignKey, manager *Manager) {
				fixture.releases[0].Assets = append(fixture.releases[0].Assets, Asset{Name: checksumSignatureAssetName})
				manager.MinisignPublicKey = key.publicKeyFile()
			},
		},
		{
			name: "wrong trusted key",
			setup: func(fixture *releaseFixture, key testMinisignKey, manager *Manager) {
				fixture.sign(key, "0.3.0")
				manager.MinisignPublicKey = newTestMinisignKey(99).publicKeyFile()
			},
		},
		{
			name: "signature for another manifest",
			setup: func(fixture *releaseFixture, key testMinisignKey, manager *Manager) {
				fixture.sign(key, "0.3.0")
				fixture.signature = key.sign([]byte("another manifest"), "NexTerm 0.3.0 SHA256SUMS")
				manager.MinisignPublicKey = key.publicKeyFile()
			},
		},
		{
			name: "signature for another version",
			setup: func(fixture *releaseFixture, key testMinisignKey, manager *Manager) {
				fixture.sign(key, "0.2.9")
				manager.MinisignPublicKey = key.publicKeyFile()
			},
		},
		{
			name: "malformed signature",
			setup: func(fixture *releaseFixture, key testMinisignKey, manager *Manager) {
				fixture.sign(key, "0.3.0")
				fixture.signature = "not a minisign signature"
				manager.MinisignPublicKey = key.publicKeyFile()
			},
		},
		{
			name: "required without trusted key",
			setup: func(_ *releaseFixture, _ testMinisignKey, manager *Manager) {
				manager.RequireSignedChecksums = true
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := newTestMinisignKey(10)
			fixture := linuxFixture(t, "0.3.0", false)
			executable := filepath.Join(t.TempDir(), "nexterm-desktop")
			if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			manager := &Manager{
				CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true, ExePath: executable,
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
	key := newTestMinisignKey(10)
	fixture := linuxFixture(t, "0.3.0", true)
	fixture.sign(key, "0.3.0")
	server := fixture.server(t)
	executable := filepath.Join(t.TempDir(), "nexterm-desktop")
	if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true,
		ExePath: executable, APIURL: server.URL + "/releases", MinisignPublicKey: key.publicKeyFile(),
	}
	if _, err := manager.Install(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "SHA256") {
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
	key := newTestMinisignKey(10)
	fixture := linuxFixture(t, "0.3.0", false)
	fixture.sign(key, "0.3.0")
	server := fixture.server(t)
	executable := filepath.Join(t.TempDir(), "nexterm-desktop")
	if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true,
		ExePath: executable, APIURL: server.URL + "/releases", MinisignPublicKey: key.publicKeyFile(), RequireSignedChecksums: true,
		RestartFunc: func(context.Context, string) error { return nil },
	}
	result, err := manager.Install(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Installed || !result.Restarted || result.Version != "0.3.0" {
		t.Fatalf("result = %+v", result)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new-binary-0.3.0" {
		t.Fatalf("executable content = %q", content)
	}
}
