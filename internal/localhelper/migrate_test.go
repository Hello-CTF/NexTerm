package localhelper

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// migrationWorlds scripts two state directories: the shared one and the
// previous per-app one, each with its own helper.
func migrationWorlds(t *testing.T) (shared, legacy *fakeWorld, config Config) {
	t.Helper()
	shared, legacy = newFakeWorld(), newFakeWorld()
	config = shared.config(t)
	legacyConfig := legacy.config(t)
	sharedDir := config.StateDir
	legacyDir := filepath.Join(filepath.Dir(sharedDir), "legacy")
	config.LegacyStateDir = legacyDir
	sharedConfig := shared.config(t)
	pick := func(dir string) Config {
		if dir == legacyDir {
			return legacyConfig
		}
		return sharedConfig
	}
	config.inspect = func(ctx context.Context, dir string) (supervisor.RunningHelper, error) {
		return pick(dir).inspect(ctx, dir)
	}
	config.stop = func(ctx context.Context, dir string, expected supervisor.RunningHelper) error {
		return pick(dir).stop(ctx, dir, expected)
	}
	config.connect = func(ctx context.Context, hc supervisor.HelperConfig) (helperHandle, error) {
		return pick(hc.StateDir).connect(ctx, hc)
	}
	return shared, legacy, config
}

func waitDir(t *testing.T, service *Service, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for service.StateDir() != want {
		if time.Now().After(deadline) {
			t.Fatalf("state dir = %s, want %s (%+v)", service.StateDir(), want, service.Status())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func currentHelper(pid, sessions int) *supervisor.RunningHelper {
	return &supervisor.RunningHelper{
		Protocol: supervisor.ProtocolVersion, PID: pid, ActivityKnown: true,
		Activity: supervisor.ActivitySnapshot{PTY: sessions}, StateDigest: "abcd1234",
		Runtime: &supervisor.RuntimeMetadata{Version: "dev", SHA256: "self"},
	}
}

func TestServiceUsesTheSharedDirWhenNoPreviousHelperRuns(t *testing.T) {
	shared, _, config := migrationWorlds(t)
	service := startService(t, config)
	ready(t, service)
	if service.StateDir() == config.LegacyStateDir || shared.connectCount() != 1 {
		t.Fatalf("dir %s, shared connects %d", service.StateDir(), shared.connectCount())
	}
}

func TestServiceStaysOnThePreviousHelperWhileItRunsSessionsThenSwitches(t *testing.T) {
	shared, legacy, config := migrationWorlds(t)
	legacy.setRunning(currentHelper(11, 2))
	service := startService(t, config)
	ready(t, service)
	if service.StateDir() != config.LegacyStateDir {
		t.Fatalf("dir = %s, want the previous one while it runs sessions", service.StateDir())
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(service.Status().Warning, "2 个会话") {
		if time.Now().After(deadline) {
			t.Fatalf("status lacks the remaining session notice: %+v", service.Status())
		}
		time.Sleep(2 * time.Millisecond)
	}
	if legacy.stopCount() != 0 || shared.connectCount() != 0 {
		t.Fatalf("legacy stops %d, shared connects %d", legacy.stopCount(), shared.connectCount())
	}
	// The last session ends: the idle previous helper is stopped and the shared one takes over.
	legacy.setRunning(currentHelper(11, 0))
	waitDir(t, service, filepath.Join(filepath.Dir(config.LegacyStateDir), "state"))
	if legacy.stopCount() != 1 {
		t.Fatalf("legacy stops = %d, want the idle helper stopped once", legacy.stopCount())
	}
	ready(t, service)
	deadline = time.Now().Add(10 * time.Second)
	for shared.connectCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the shared helper never started")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if warning := service.Status().Warning; strings.Contains(warning, "上一版") {
		t.Fatalf("stale migration notice: %q", warning)
	}
}

func TestServiceNeverStopsAPreviousHelperWithUnknownSessions(t *testing.T) {
	_, legacy, config := migrationWorlds(t)
	running := currentHelper(11, 0)
	running.ActivityKnown = false
	legacy.setRunning(running)
	service := startService(t, config)
	ready(t, service)
	if service.StateDir() != config.LegacyStateDir || legacy.stopCount() != 0 {
		t.Fatalf("dir %s, stops %d", service.StateDir(), legacy.stopCount())
	}
}

func TestServiceDoesNotOfferToReplaceANewerHelper(t *testing.T) {
	world := newFakeWorld()
	world.running = currentHelper(11, 3)
	world.running.Runtime = &supervisor.RuntimeMetadata{Version: "9.0.0", SHA256: "newer"}
	config := world.config(t)
	config.selfVersion = func() string { return "1.0.0" }
	service := startService(t, config)
	ready(t, service)
	status := waitState(t, service, StateReady)
	if status.UpdateAvailable || status.ConsentToken != "" {
		t.Fatalf("a newer helper was offered for replacement: %+v", status)
	}
	if err := service.Restart(context.Background(), "1:11:3:1::abcd1234"); err == nil || world.stopCount() != 0 {
		t.Fatalf("restart of a newer helper: err %v, stops %d", err, world.stopCount())
	}
}

func TestServiceTreatsTheSameReleaseOfAnotherExecutableAsCurrent(t *testing.T) {
	world := newFakeWorld()
	world.running = currentHelper(11, 3)
	world.running.Runtime = &supervisor.RuntimeMetadata{Version: "1.4.0", Commit: "abc", SHA256: "agent-binary"}
	config := world.config(t)
	config.selfVersion = func() string { return "1.4.0" }
	config.selfCommit = func() string { return "abc" }
	service := startService(t, config)
	ready(t, service)
	if status := waitState(t, service, StateReady); status.UpdateAvailable || status.ConsentToken != "" {
		t.Fatalf("same release offered as an update: %+v", status)
	}
	// A different commit of the same version still is one.
	world2 := newFakeWorld()
	world2.running = currentHelper(12, 0)
	world2.running.Runtime = &supervisor.RuntimeMetadata{Version: "1.4.0", Commit: "old", SHA256: "agent-binary"}
	config2 := world2.config(t)
	config2.selfVersion = func() string { return "1.4.0" }
	config2.selfCommit = func() string { return "abc" }
	service2 := startService(t, config2)
	ready(t, service2)
	if status := waitState(t, service2, StateReady); !status.UpdateAvailable {
		t.Fatalf("other commit not offered: %+v", status)
	}
}

func TestOutdatedPreviousHelperReportsItsDirectoryInTheWarning(t *testing.T) {
	_, legacy, config := migrationWorlds(t)
	old := currentHelper(11, 2)
	old.Protocol = supervisor.ProtocolVersion - 1
	legacy.setRunning(old)
	service := startService(t, config)
	status := waitState(t, service, StateOutdated)
	if !strings.Contains(status.Warning, "2 个会话") {
		t.Fatalf("status = %+v, want the previous-directory notice in Warning", status)
	}
}
