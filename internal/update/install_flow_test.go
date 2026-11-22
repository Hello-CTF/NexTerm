package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// halfPublished adds releases newer than the fixture's: one without SHA256SUMS and one without any package for
// this platform, as GitHub shows a release whose assets are still uploading.
func halfPublished(fixture *releaseFixture, versions ...string) {
	for index, version := range versions {
		release := Release{TagName: "v" + version, Body: "notes " + version}
		if index%2 == 0 {
			release.Assets = []Asset{{Name: "nexterm-desktop_linux_amd64_" + version + ".deb", Size: 1}}
		} else {
			release.Assets = []Asset{{Name: checksumsAssetName}}
		}
		fixture.releases = append([]Release{release}, fixture.releases...)
	}
}

func TestCheckSkipsHalfPublishedReleases(t *testing.T) {
	fixture := linuxFixture(t, "0.3.0", false)
	fixture.releases[0].HTMLURL = "https://example.invalid/releases/v0.3.0"
	halfPublished(fixture, "0.4.0", "0.5.0")
	server := fixture.server(t)
	manager := &Manager{CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", APIURL: server.URL + "/releases"}
	status := manager.Check(context.Background())
	if !status.Available || status.Version != "0.3.0" || status.UnavailableReason != "" {
		t.Fatalf("status = %+v, want the newest complete release", status)
	}
	if status.PendingVersion != "0.5.0" {
		t.Fatalf("pending version = %q, want the newest skipped release", status.PendingVersion)
	}
	if status.ReleaseURL != "https://example.invalid/releases/v0.3.0" || !strings.HasSuffix(status.DownloadURL, fixture.archiveName) {
		t.Fatalf("links = %q, %q", status.ReleaseURL, status.DownloadURL)
	}
}

func TestCheckReportsOnlyHalfPublishedReleaseAsPending(t *testing.T) {
	fixture := linuxFixture(t, "0.3.0", false)
	server := fixture.server(t)
	manager := &Manager{CurrentVersion: "0.2.2", GOOS: "windows", GOARCH: "amd64", APIURL: server.URL + "/releases"}
	status := manager.Check(context.Background())
	if status.Available || status.UnavailableReason != "" || status.PendingVersion != "0.3.0" {
		t.Fatalf("status = %+v, want no failure and the pending version", status)
	}
}

func TestCheckDoesNotFallBackWhenChecksumsFail(t *testing.T) {
	fixture := linuxFixture(t, "0.3.0", false)
	newer := linuxFixture(t, "0.4.0", false)
	fixture.releases = append(newer.releases, fixture.releases...)
	server := fixture.server(t)
	for index := range fixture.releases[0].Assets {
		asset := &fixture.releases[0].Assets[index]
		if asset.Name == checksumsAssetName {
			asset.BrowserDownloadURL = server.URL + "/assets/missing"
		}
	}
	manager := &Manager{CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", APIURL: server.URL + "/releases"}
	status := manager.Check(context.Background())
	if status.Available || !strings.Contains(status.UnavailableReason, "获取校验和失败") {
		t.Fatalf("status = %+v, want a failed check rather than the older release", status)
	}
}

func TestProgressThrottlesOnlyIntermediateTicks(t *testing.T) {
	now := time.Unix(1000, 0)
	var events []Progress
	manager := &Manager{
		now:  func() time.Time { return now },
		Emit: func(_ context.Context, event Progress) { events = append(events, event) },
	}
	ctx := context.Background()
	manager.report(ctx, Progress{TaskID: "t", Phase: PhaseDownload, Total: 100}, false)
	for transferred := int64(1); transferred <= 10; transferred++ {
		now = now.Add(10 * time.Millisecond)
		manager.report(ctx, Progress{TaskID: "t", Phase: PhaseDownload, Transferred: transferred, Total: 100}, true)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want ticks within the interval dropped", events)
	}
	if state := manager.State(); state.Transferred != 10 {
		t.Fatalf("state = %+v, want every tick recorded", state)
	}
	now = now.Add(progressInterval)
	manager.report(ctx, Progress{TaskID: "t", Phase: PhaseDownload, Transferred: 50, Total: 100}, true)
	manager.report(ctx, Progress{TaskID: "t", Phase: PhaseDownload, Transferred: 100, Total: 100, Done: true}, false)
	manager.report(ctx, Progress{TaskID: "t", Phase: PhaseVerify, Transferred: 100, Total: 100}, false)
	if len(events) != 4 || !events[2].Done || events[3].Phase != PhaseVerify {
		t.Fatalf("events = %+v, want the due tick, completion and the phase change", events)
	}
}

func TestInstallFailureIsKeptInState(t *testing.T) {
	fixture := linuxFixture(t, "0.3.0", true)
	server := fixture.server(t)
	executable := filepath.Join(t.TempDir(), "nexterm-desktop")
	if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true,
		ExePath: executable, APIURL: server.URL + "/releases",
	}
	if _, err := manager.Install(context.Background(), ""); err == nil {
		t.Fatal("install must fail on a checksum mismatch")
	}
	state := manager.State()
	if state.Phase != StateFailed || state.Version != "0.3.0" || !strings.Contains(state.Error, "SHA-256") || state.InstalledVersion != "" {
		t.Fatalf("state = %+v", state)
	}
}

func TestInstallSurvivesCallerCancellation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux deb install test")
	}
	fixture := linuxFixture(t, "0.3.0", false)
	server := fixture.server(t)
	executable := filepath.Join(t.TempDir(), "nexterm-desktop")
	if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true,
		ExePath: executable, APIURL: server.URL + "/releases",
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager.Emit = func(context.Context, Progress) { cancel() }
	if _, err := manager.Install(ctx, ""); err != nil {
		t.Fatalf("a reloaded interface must not abort the install: %v", err)
	}
}

func TestRestartWaitsForRunningInstall(t *testing.T) {
	restarted := false
	manager := &Manager{ExePath: "/opt/nexterm/nexterm-desktop", RestartFunc: func(context.Context, string) error {
		restarted = true
		return nil
	}}
	manager.installing.Store(true)
	if result := manager.Restart(context.Background()); result.Restarted || result.UnavailableReason == "" || restarted {
		t.Fatalf("restart during install = %+v", result)
	}
	manager.installing.Store(false)
	if result := manager.Restart(context.Background()); !result.Restarted || !restarted {
		t.Fatalf("restart after install = %+v", result)
	}
}

func TestMacInstallTarget(t *testing.T) {
	writable := func(string) error { return nil }
	readOnly := func(string) error { return os.ErrPermission }
	tests := []struct {
		name       string
		executable string
		writable   func(string) error
		want       string
	}{
		{"keeps a bundle outside Applications", "/Users/a/Apps/NexTerm Beta.app/Contents/MacOS/nexterm-desktop", writable, "/Users/a/Apps/NexTerm Beta.app"},
		{"keeps the installed bundle", "/Applications/NexTerm.app/Contents/MacOS/nexterm-desktop", writable, "/Applications/NexTerm.app"},
		{"leaves a disk image", "/Volumes/NexTerm/NexTerm.app/Contents/MacOS/nexterm-desktop", writable, "/Applications/NexTerm.app"},
		{"leaves App Translocation", "/private/var/folders/x/AppTranslocation/1/d/NexTerm.app/Contents/MacOS/nexterm-desktop", writable, "/Applications/NexTerm.app"},
		{"leaves an unwritable directory", "/opt/apps/NexTerm.app/Contents/MacOS/nexterm-desktop", readOnly, "/Applications/NexTerm.app"},
		{"falls back outside a bundle", "/usr/local/bin/nexterm-desktop", writable, "/Applications/NexTerm.app"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := macInstallTarget(test.executable, "NexTerm.app", test.writable); got != filepath.FromSlash(test.want) {
				t.Fatalf("macInstallTarget() = %q, want %q", got, test.want)
			}
		})
	}
}

func writeBundle(t *testing.T, bundle, marker string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(bundle, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "MacOS", "nexterm-desktop"), []byte(marker), 0o755); err != nil {
		t.Fatal(err)
	}
}

func bundleMarker(t *testing.T, bundle string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bundle, "Contents", "MacOS", "nexterm-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func requireNoStaging(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".nexterm-update-") {
			t.Fatalf("staging directory %s left behind", entry.Name())
		}
	}
}

func TestReplaceBundleSwapsAfterCopy(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "NexTerm.app")
	writeBundle(t, target, "old")
	err := replaceBundle(target, func(staged string) error {
		if bundleMarker(t, target) != "old" {
			t.Fatal("the installed bundle must stay in place while the new one is copied")
		}
		writeBundle(t, staged, "new")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := bundleMarker(t, target); got != "new" {
		t.Fatalf("bundle = %q, want new", got)
	}
	requireNoStaging(t, directory)
}

func TestReplaceBundleKeepsOldBundleWhenCopyFails(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "NexTerm.app")
	writeBundle(t, target, "old")
	copyErr := errors.New("disk full")
	err := replaceBundle(target, func(staged string) error {
		writeBundle(t, staged, "partial")
		return copyErr
	})
	if !errors.Is(err, copyErr) {
		t.Fatalf("err = %v", err)
	}
	if got := bundleMarker(t, target); got != "old" {
		t.Fatalf("bundle = %q, want old", got)
	}
	requireNoStaging(t, directory)
}

func TestReplaceBundleInstallsWithoutPreviousBundle(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "NexTerm.app")
	if err := replaceBundle(target, func(staged string) error {
		writeBundle(t, staged, "new")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := bundleMarker(t, target); got != "new" {
		t.Fatalf("bundle = %q, want new", got)
	}
}
