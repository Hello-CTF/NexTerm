package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/version"
)

// After a rollback the restored agent is asked to upgrade to the same version again; it must not download it again
// at once (a replace-rollback loop), must report the rollback to the server through sync, and must stay alive and
// retry once the backoff has passed instead of exiting (a service manager would not restart it).
func TestRuntimeSelfUpgradePostponesAfterRollbackThenRetries(t *testing.T) {
	const target = "1.2.3"
	server := newFakeFleetServer(t)
	server.setWSControl(mismatchControl(target))
	release := newReleaseServer(t, map[string][]byte{})
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	executable := filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS))
	writeExecutable(t, executable, "old-binary")
	clock := time.Unix(1_800_000_000, 0)
	var clockMu sync.Mutex
	nowFn := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	failedAt := nowFn().Add(-10 * time.Minute)
	record := fmt.Sprintf("%s %d %s\n", target, failedAt.Unix(), upgradeFailureUnhealthy)
	if err := os.WriteFile(executable+upgradeFailedSuffix, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	upgrader := &Upgrader{
		PublicKey: testReleaseKey.PublicKeyFile(), BaseURL: release.URL, Executable: executable,
		MaxAttempts: 1, sleep: instantSleep, verifyStaged: skipStagedCheck, scheduleReplace: func(string, string) error { return nil },
	}
	agentRuntime := newUpgradeTestRuntime(t, dataDir, entries, upgrader)
	agentRuntime.now = nowFn
	delays := make(chan time.Duration, 4)
	downloadsBeforeSleepEnds := make(chan int, 1)
	agentRuntime.sleep = func(ctx context.Context, d time.Duration) bool {
		if d < 30*time.Minute {
			return sleepContext(ctx, d)
		}
		reportDeadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(reportDeadline) {
			for _, request := range server.syncs() {
				if request.ServiceState != nil && strings.Contains(request.ServiceState.LastError, target) {
					reportDeadline = time.Time{}
				}
			}
			if reportDeadline.IsZero() {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		downloadsBeforeSleepEnds <- release.requestCount("/")
		delays <- d
		clockMu.Lock()
		clock = clock.Add(d)
		clockMu.Unlock()
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := agentRuntime.Run(ctx)
	var failed *selfUpgradeFailedError
	if !errors.Is(err, ErrProtocolMismatch) || !errors.As(err, &failed) {
		t.Fatalf("Run = %v, want the retried upgrade to fail after the backoff", err)
	}
	if got := <-downloadsBeforeSleepEnds; got != 0 {
		t.Fatalf("a recently rolled-back version must not be downloaded during the backoff, got %d requests", got)
	}
	if got, want := <-delays, upgradeFailureBackoff-10*time.Minute; got != want {
		t.Fatalf("waited %s, want the remaining backoff %s", got, want)
	}
	if got := release.requestCount("/v" + target + "/"); got == 0 {
		t.Fatal("the upgrade must be tried again once the backoff passed")
	}
	reported := false
	for _, request := range server.syncs() {
		if request.ServiceState != nil && strings.Contains(request.ServiceState.LastError, target) {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("the rollback must be reported through sync, got %+v", server.syncs())
	}
}

// Stopping the agent during the backoff wait ends the run cleanly without an upgrade attempt.
func TestRuntimeSelfUpgradeBackoffWaitIsInterruptible(t *testing.T) {
	const target = "1.2.3"
	server := newFakeFleetServer(t)
	server.setWSControl(mismatchControl(target))
	release := newReleaseServer(t, map[string][]byte{})
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	executable := filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS))
	writeExecutable(t, executable, "old-binary")
	record := fmt.Sprintf("%s %d %s\n", target, time.Now().Unix(), upgradeFailureUnhealthy)
	if err := os.WriteFile(executable+upgradeFailedSuffix, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	upgrader := &Upgrader{
		PublicKey: testReleaseKey.PublicKeyFile(), BaseURL: release.URL, Executable: executable,
		MaxAttempts: 1, sleep: instantSleep, verifyStaged: skipStagedCheck, scheduleReplace: func(string, string) error { return nil },
	}
	agentRuntime := newUpgradeTestRuntime(t, dataDir, entries, upgrader)
	waiting := make(chan struct{}, 1)
	agentRuntime.sleep = func(ctx context.Context, d time.Duration) bool {
		if d < time.Hour-time.Minute {
			return sleepContext(ctx, d)
		}
		select {
		case waiting <- struct{}{}:
		default:
		}
		return sleepContext(ctx, time.Minute)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentRuntime.Run(ctx) }()
	select {
	case <-waiting:
	case err := <-done:
		t.Fatalf("Run returned %v instead of waiting out the backoff", err)
	case <-time.After(15 * time.Second):
		t.Fatal("the backoff wait never started")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want a clean stop", err)
	}
	if got := release.requestCount("/"); got != 0 {
		t.Fatalf("no download expected, got %d requests", got)
	}
}

// Once the backoff of a rolled-back version has passed, the server-driven upgrade is tried again.
func TestRuntimeSelfUpgradeRetriesAfterBackoff(t *testing.T) {
	const target = "1.2.3"
	server := newFakeFleetServer(t)
	server.setWSControl(mismatchControl(target))
	release := newReleaseServer(t, map[string][]byte{})
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	executable := filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS))
	writeExecutable(t, executable, "old-binary")
	old := time.Now().Add(-2 * upgradeFailureBackoff).Unix()
	if err := os.WriteFile(executable+upgradeFailedSuffix, []byte(fmt.Sprintf("%s %d %s\n", target, old, upgradeFailureUnhealthy)), 0o600); err != nil {
		t.Fatal(err)
	}
	upgrader := &Upgrader{
		PublicKey: testReleaseKey.PublicKeyFile(), BaseURL: release.URL, Executable: executable,
		MaxAttempts: 1, sleep: instantSleep, verifyStaged: skipStagedCheck, scheduleReplace: func(string, string) error { return nil },
	}
	agentRuntime := newUpgradeTestRuntime(t, dataDir, entries, upgrader)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := agentRuntime.Run(ctx); !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("Run = %v", err)
	}
	if got := release.requestCount("/v" + target + "/"); got == 0 {
		t.Fatal("the upgrade must be tried again once the backoff passed")
	}
}

// The upgraded agent confirms itself to the waiting replace script only after its control channel connects.
func TestRuntimeWritesHealthMarkerAfterChannelConnects(t *testing.T) {
	previous := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = previous })
	server := newFakeFleetServer(t)
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	executable := filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS))
	writeExecutable(t, executable, "new-binary")
	writeExecutable(t, executable+upgradeBackupSuffix, "old-binary")
	if err := os.WriteFile(executable+upgradeFailedSuffix, []byte(fmt.Sprintf("1.2.3 %d %s\n", time.Now().Unix(), upgradeFailureUnhealthy)), 0o600); err != nil {
		t.Fatal(err)
	}
	upgrader := &Upgrader{Executable: executable, BaseURL: "http://127.0.0.1:1"}
	agentRuntime := newUpgradeTestRuntime(t, dataDir, entries, upgrader)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentRuntime.Run(ctx) }()
	deadline := time.Now().Add(15 * time.Second)
	var marker []byte
	for time.Now().Before(deadline) {
		if content, err := os.ReadFile(executable + upgradeHealthySuffix); err == nil {
			marker = content
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if string(marker) != "1.2.3" {
		t.Fatalf("health marker = %q, want the running version", marker)
	}
	if failures := readUpgradeFailures(executable); len(failures) != 0 {
		t.Fatalf("a healthy upgrade forgets its version's failures, got %+v", failures)
	}
}
