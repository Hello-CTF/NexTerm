package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/version"
)

// After a rollback the restored agent is asked to upgrade to the same version again; it must not download it again
// at once (a replace-rollback loop) and must report the rollback to the server through sync.
func TestRuntimeSelfUpgradeBacksOffAfterRollback(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := agentRuntime.Run(ctx)
	var failed *selfUpgradeFailedError
	if !errors.Is(err, ErrProtocolMismatch) || !errors.As(err, &failed) || !strings.Contains(failed.cause.Error(), target) {
		t.Fatalf("Run = %v, want a postponed self-upgrade naming %s", err, target)
	}
	if got := release.requestCount("/"); got != 0 {
		t.Fatalf("a recently rolled-back version must not be downloaded again, got %d requests", got)
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
