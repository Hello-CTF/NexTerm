//go:build linux

package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxInstallBlockedBeforeDownload(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, executable string)
		contains string
	}{
		{
			name: "deb package owns the executable",
			setup: func(t *testing.T, executable string) {
				infoDir := t.TempDir()
				list := "/.\n/usr\n/usr/bin\n" + executable + "\n"
				if err := os.WriteFile(filepath.Join(infoDir, "nexterm.list"), []byte(list), 0o644); err != nil {
					t.Fatal(err)
				}
				previous := dpkgInfoDir
				dpkgInfoDir = infoDir
				t.Cleanup(func() { dpkgInfoDir = previous })
			},
			contains: "deb",
		},
		{
			name: "directory not writable",
			setup: func(t *testing.T, executable string) {
				if os.Geteuid() == 0 {
					t.Skip("root can write any directory")
				}
				directory := filepath.Dir(executable)
				if err := os.Chmod(directory, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(directory, 0o755) })
			},
			contains: "不可写",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := dpkgInfoDir
			dpkgInfoDir = t.TempDir()
			t.Cleanup(func() { dpkgInfoDir = previous })
			fixture := linuxFixture(t, "0.3.0", false)
			server := newObservedReleaseServer(t, fixture, nil)
			executable := filepath.Join(t.TempDir(), "nexterm-desktop")
			if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			test.setup(t, executable)
			manager := &Manager{
				CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true,
				ExePath: executable, APIURL: server.server.URL + "/releases",
			}
			status := manager.Check(context.Background())
			if !status.Available || status.CanInstall || !strings.Contains(status.UnavailableReason, test.contains) {
				t.Fatalf("status = %+v, want an available but not installable update", status)
			}
			if !strings.HasSuffix(status.DownloadURL, ".deb") {
				t.Fatalf("download URL = %q, want the deb for a manual install", status.DownloadURL)
			}
			result, err := manager.Install(context.Background(), "")
			if err != nil || result.Installed || !strings.Contains(result.UnavailableReason, test.contains) {
				t.Fatalf("install = %+v, %v", result, err)
			}
			if requests := server.artifactRequests.Load(); requests != 0 {
				t.Fatalf("artifact requests = %d, want nothing downloaded", requests)
			}
		})
	}
}
