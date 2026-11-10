package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// skipStagedCheck stands in for checkStagedVersion in tests whose staged binary is not runnable.
func skipStagedCheck(context.Context, string, string) error { return nil }

func requireUnixShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix shell script")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh unavailable")
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestNewReplacePlanKeepsStateBesideTheBinary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nexterm-agent")
	plan := newReplacePlan(filepath.Join(dir, ".nexterm-agent.nexterm-new"), target, "1.2.3", filepath.Join(dir, "upgrade.log"), 4242)
	if plan.TargetPath != target || plan.Version != "1.2.3" || plan.WaitPID != 4242 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.WaitTimeoutSeconds != replaceWaitTimeoutSeconds || plan.HealthTimeoutSeconds != replaceHealthTimeoutSeconds {
		t.Fatalf("plan timeouts = %d/%d", plan.WaitTimeoutSeconds, plan.HealthTimeoutSeconds)
	}
	for _, path := range []string{plan.BackupPath(), plan.MarkerPath(), plan.FailurePath()} {
		if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "nexterm-agent.") {
			t.Fatalf("replace state %q must sit next to the binary", path)
		}
	}
	if plan.BackupPath() == plan.MarkerPath() || plan.MarkerPath() == plan.FailurePath() {
		t.Fatal("backup, marker and failure record must be distinct files")
	}

	windows := newWindowsReplaceScript(plan, windowsTaskName)
	if windows.replacePlan != plan {
		t.Fatalf("windows script plan = %+v", windows.replacePlan)
	}
	if windows.RestartBlock != windowsRestartBlock(windowsTaskName) || windows.StopBlock != windowsStopBlock(windowsTaskName) {
		t.Fatal("windows script must restart and stop the agent task")
	}
}

// Markers the agent writes must be found by the script for the same target, also through a symlink.
func TestMarkUpgradeHealthyOnlyDuringAReplacement(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nexterm-agent")
	writeExecutable(t, target, "binary")
	plan := newReplacePlan(target+".new", target, "1.2.3", "", 0)

	written, err := markUpgradeHealthy(target, "1.2.3")
	if err != nil || written {
		t.Fatalf("without a backup: written=%v err=%v", written, err)
	}
	if _, err := os.Stat(plan.MarkerPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a regular start must not leave a marker")
	}

	writeExecutable(t, plan.BackupPath(), "old")
	record := fmt.Sprintf("1.2.3 %d unhealthy\n1.2.2 %d unhealthy\n", time.Now().Unix(), time.Now().Unix())
	if err := os.WriteFile(plan.FailurePath(), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "agent-link")
	if err := os.Symlink(target, link); err != nil {
		link = target
	}
	written, err = markUpgradeHealthy(link, "1.2.3")
	if err != nil || !written {
		t.Fatalf("during a replacement: written=%v err=%v", written, err)
	}
	content, err := os.ReadFile(plan.MarkerPath())
	if err != nil || string(content) != "1.2.3" {
		t.Fatalf("marker = %q, %v", content, err)
	}
	requireMissing(t, plan.BackupPath(), "the backup is dropped once the new binary is confirmed")
	failures := readUpgradeFailures(target)
	if len(failures) != 1 || failures[0].Version != "1.2.2" {
		t.Fatalf("only the healthy version's failures are forgotten, got %+v", failures)
	}
}

func TestUpgradeRetryAfterBacksOffPerVersion(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	failures := []upgradeFailure{
		{Version: "1.2.3", At: base, Reason: upgradeFailureUnhealthy},
		{Version: "1.2.4", At: base.Add(time.Minute), Reason: upgradeFailureUnhealthy},
	}
	if _, _, ok := upgradeRetryAfter(failures, "1.3.0"); ok {
		t.Fatal("a version that never failed has no backoff")
	}
	retry, last, ok := upgradeRetryAfter(failures, "1.2.3")
	if !ok || last.Version != "1.2.3" || !retry.Equal(base.Add(upgradeFailureBackoff)) {
		t.Fatalf("first failure: retry=%v last=%+v ok=%v", retry, last, ok)
	}
	second := append(failures, upgradeFailure{Version: "1.2.3", At: base.Add(2 * time.Hour)})
	retry, _, _ = upgradeRetryAfter(second, "1.2.3")
	if want := base.Add(2*time.Hour + 2*upgradeFailureBackoff); !retry.Equal(want) {
		t.Fatalf("second failure retry = %v, want %v", retry, want)
	}
	many := failures
	for i := range 40 {
		many = append(many, upgradeFailure{Version: "1.2.3", At: base.Add(time.Duration(i) * time.Second)})
	}
	retry, last, _ = upgradeRetryAfter(many, "1.2.3")
	if want := last.At.Add(upgradeFailureMaxBackoff); !retry.Equal(want) {
		t.Fatalf("backoff must cap at %s: retry = %v, want %v", upgradeFailureMaxBackoff, retry, want)
	}
}

func TestCheckStagedVersion(t *testing.T) {
	requireUnixShell(t)
	dir := t.TempDir()
	script := func(name, body string) string {
		path := filepath.Join(dir, name)
		writeExecutable(t, path, "#!/bin/sh\n"+body+"\n")
		return path
	}
	ctx := context.Background()

	if err := checkStagedVersion(ctx, script("plain", `[ "$1" = version ] && echo 1.2.3`), "1.2.3"); err != nil {
		t.Fatalf("matching version: %v", err)
	}
	if err := checkStagedVersion(ctx, script("prefixed", "echo v1.2.3"), "1.2.3"); err != nil {
		t.Fatalf("v-prefixed version: %v", err)
	}
	err := checkStagedVersion(ctx, script("other", "echo 1.2.4"), "1.2.3")
	if !errors.Is(err, errUpgradeRefused) || !strings.Contains(err.Error(), "1.2.4") {
		t.Fatalf("mismatched version must be refused naming the reported version, got %v", err)
	}
	err = checkStagedVersion(ctx, script("crash", "echo boom >&2; exit 3"), "1.2.3")
	if err == nil || errors.Is(err, errUpgradeRefused) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a binary that fails to run must fail with its output and stay retryable, got %v", err)
	}
	notExecutable := filepath.Join(dir, "data")
	if err := os.WriteFile(notExecutable, []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkStagedVersion(ctx, notExecutable, "1.2.3"); err == nil {
		t.Fatal("a file that cannot be executed must fail the check")
	}

	previous := stagedVersionTimeout
	stagedVersionTimeout = 200 * time.Millisecond
	t.Cleanup(func() { stagedVersionTimeout = previous })
	err = checkStagedVersion(ctx, script("hang", "exec sleep 30"), "1.2.3")
	if err == nil || errors.Is(err, errUpgradeRefused) {
		t.Fatalf("a hanging binary must fail the check, got %v", err)
	}
}

// The real check runs during Upgrade: a staged binary reporting another version is never scheduled and not retried.
func TestUpgradeRefusesStagedBinaryReportingAnotherVersion(t *testing.T) {
	requireUnixShell(t)
	version := "1.2.3"
	asset := releaseAssetName(version, runtime.GOOS, runtime.GOARCH)
	for _, tc := range []struct {
		name    string
		binary  string
		wantErr bool
	}{
		{name: "matching", binary: "#!/bin/sh\necho 1.2.3\n"},
		{name: "mismatched", binary: "#!/bin/sh\necho 1.2.2\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := buildReleaseArchive(t, runtime.GOOS, []byte(tc.binary))
			server := newReleaseServer(t, mergeFiles(map[string][]byte{"/v" + version + "/" + asset: archive},
				signedChecksums(version, fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset))))
			target := filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS))
			writeExecutable(t, target, "old-binary")
			var replaced []stagedReplace
			upgrader := &Upgrader{
				BaseURL: server.URL, Executable: target, PublicKey: testReleaseKey.PublicKeyFile(),
				MaxAttempts: 3, Backoff: time.Millisecond, sleep: instantSleep, scheduleReplace: captureReplace(&replaced),
			}
			err := upgrader.Upgrade(context.Background(), version)
			if !tc.wantErr {
				if err != nil || len(replaced) != 1 {
					t.Fatalf("Upgrade = %v, replaced %d", err, len(replaced))
				}
				return
			}
			if !errors.Is(err, errUpgradeRefused) || len(replaced) != 0 {
				t.Fatalf("Upgrade = %v, replaced %d; want a refusal without scheduling", err, len(replaced))
			}
			if got := server.requestCount("/v" + version + "/" + asset); got != 1 {
				t.Fatalf("a version mismatch must not be retried, downloads = %d", got)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".nexterm-new")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("the refused staged binary must be removed")
			}
		})
	}
}

func TestUnixRestartCommandReplacesARunningInstance(t *testing.T) {
	if got := unixRestartCommand("darwin"); !strings.Contains(got, "kickstart -k") || !strings.Contains(got, launchdLabel) {
		t.Fatalf("darwin restart = %q, want a kickstart -k of the agent label", got)
	}
	if got := unixRestartCommand("linux"); got != "systemctl --user restart "+systemdUnitName {
		t.Fatalf("linux restart = %q", got)
	}
}

// replaceFixture runs the rendered Unix script against fake binaries in a temp dir. The restart command stands in for
// the service manager by running the installed binary once; a healthy fake new agent writes the marker like the
// real one does after connecting.
type replaceFixture struct {
	dir, target, newPath, logPath, started string
	plan                                   replacePlan
}

func newReplaceFixture(t *testing.T) *replaceFixture {
	t.Helper()
	dir := t.TempDir()
	f := &replaceFixture{
		dir: dir, target: filepath.Join(dir, "nexterm-agent"), newPath: filepath.Join(dir, ".nexterm-agent.nexterm-new"),
		logPath: filepath.Join(dir, "upgrade.log"), started: filepath.Join(dir, "started"),
	}
	f.plan = newReplacePlan(f.newPath, f.target, "1.2.3", f.logPath, 0)
	f.plan.HealthTimeoutSeconds = 1
	writeExecutable(t, f.target, "#!/bin/sh\necho old >>"+shQuote(f.started)+"\n")
	return f
}

// newAgent installs a fake new binary that records its start and, when marker is not empty, writes it as its health marker.
func (f *replaceFixture) newAgent(t *testing.T, marker string, exit int) {
	t.Helper()
	body := "#!/bin/sh\necho new >>" + shQuote(f.started) + "\n"
	if marker != "" {
		body += "printf '%s' " + shQuote(marker) + " >\"$0" + upgradeHealthySuffix + "\"\n"
	}
	body += fmt.Sprintf("exit %d\n", exit)
	writeExecutable(t, f.newPath, body)
}

func (f *replaceFixture) run(t *testing.T, restart string) {
	t.Helper()
	scriptPath := filepath.Join(f.dir, "replace.sh")
	writeExecutable(t, scriptPath, renderReplaceScriptUnix(f.plan, restart))
	if output, err := exec.Command("sh", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, output)
	}
	if _, err := os.Stat(scriptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("script must remove itself")
	}
}

// serviceRestart starts the installed binary like a service manager does: the start succeeds whatever the agent does next.
func (f *replaceFixture) serviceRestart() string { return shQuote(f.target) + " run || true" }

func (f *replaceFixture) read(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(content)
}

func (f *replaceFixture) installed(t *testing.T) string {
	t.Helper()
	content := f.read(t, f.target)
	switch {
	case strings.Contains(content, "echo new"):
		return "new"
	case strings.Contains(content, "echo old"):
		return "old"
	}
	return content
}

func requireMissing(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s: %s still exists", why, path)
	}
}

func TestReplaceScriptUnixExecutes(t *testing.T) {
	requireUnixShell(t)

	t.Run("healthy new agent is kept", func(t *testing.T) {
		f := newReplaceFixture(t)
		f.newAgent(t, "1.2.3", 0)
		f.run(t, f.serviceRestart())
		if got := f.installed(t); got != "new" {
			t.Fatalf("installed = %q, want new", got)
		}
		if got := f.read(t, f.started); got != "new\n" {
			t.Fatalf("started = %q", got)
		}
		requireMissing(t, f.plan.BackupPath(), "backup must be removed once the new agent is healthy")
		requireMissing(t, f.plan.MarkerPath(), "marker must be cleaned up")
		requireMissing(t, f.plan.FailurePath(), "a healthy upgrade records no failure")
		if got := f.read(t, f.logPath); !strings.Contains(got, upgradeLogRestartExit+" 0") || !strings.Contains(got, "healthy") {
			t.Fatalf("log = %q", got)
		}
	})

	for _, tc := range []struct {
		name   string
		marker string
		exit   int
	}{
		{name: "crashing new agent is rolled back", exit: 1},
		{name: "new agent that never connects is rolled back", exit: 0},
		{name: "marker of another version does not count", marker: "1.2.2", exit: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReplaceFixture(t)
			f.newAgent(t, tc.marker, tc.exit)
			f.run(t, f.serviceRestart())
			if got := f.installed(t); got != "old" {
				t.Fatalf("installed = %q, want the previous binary restored", got)
			}
			if got := f.read(t, f.started); got != "new\nold\n" {
				t.Fatalf("started = %q, want the new agent then the restored one", got)
			}
			requireMissing(t, f.plan.BackupPath(), "the backup is moved back into place")
			failures := readUpgradeFailures(f.target)
			if len(failures) != 1 || failures[0].Version != "1.2.3" || failures[0].Reason != upgradeFailureUnhealthy {
				t.Fatalf("failure record = %+v", failures)
			}
			if time.Since(failures[0].At) > time.Hour || time.Until(failures[0].At) > time.Minute {
				t.Fatalf("failure time = %v", failures[0].At)
			}
		})
	}

	t.Run("repeated rollbacks accumulate per version", func(t *testing.T) {
		f := newReplaceFixture(t)
		for range 2 {
			f.newAgent(t, "", 1)
			f.run(t, f.serviceRestart())
		}
		if failures := readUpgradeFailures(f.target); len(failures) != 2 {
			t.Fatalf("failure record = %+v", failures)
		}
	})

	t.Run("failing restart keeps the new binary without a health check", func(t *testing.T) {
		f := newReplaceFixture(t)
		f.newAgent(t, "", 0)
		f.run(t, "echo restart-diagnostic; false")
		if got := f.installed(t); got != "new" {
			t.Fatalf("installed = %q, want new", got)
		}
		requireMissing(t, f.plan.BackupPath(), "backup must be removed")
		requireMissing(t, f.plan.FailurePath(), "an unverified swap is not a failure")
		got := f.read(t, f.logPath)
		if !strings.Contains(got, upgradeLogRestartExit+" 1") || !strings.Contains(got, "restart-diagnostic") {
			t.Fatalf("log must record restart output and failure code, got %q", got)
		}
	})

	t.Run("unopenable log still swaps and restarts", func(t *testing.T) {
		f := newReplaceFixture(t)
		f.newAgent(t, "1.2.3", 0)
		f.plan.LogPath = filepath.Join(f.dir, "no-such-dir", "upgrade.log")
		f.run(t, f.serviceRestart())
		if got := f.installed(t); got != "new" {
			t.Fatalf("installed = %q, want new", got)
		}
		if got := f.read(t, f.started); got != "new\n" {
			t.Fatalf("restart must run even when the log cannot be opened, started = %q", got)
		}
	})

	t.Run("failed swap rolls back without restarting", func(t *testing.T) {
		f := newReplaceFixture(t)
		f.run(t, f.serviceRestart())
		if got := f.installed(t); got != "old" {
			t.Fatalf("installed = %q, want old", got)
		}
		if got := f.read(t, f.started); got != "" {
			t.Fatalf("rollback must not restart, started = %q", got)
		}
		if got := f.read(t, f.logPath); strings.Contains(got, upgradeLogRestartExit) {
			t.Fatalf("rollback must not restart, log %q", got)
		}
	})

	t.Run("waits for the upgrading process to exit", func(t *testing.T) {
		f := newReplaceFixture(t)
		f.newAgent(t, "1.2.3", 0)
		upgrading := exec.Command("sleep", "1")
		startedAt := time.Now()
		if err := upgrading.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		go func() {
			_ = upgrading.Wait()
			close(exited)
		}()
		f.plan.WaitPID = upgrading.Process.Pid
		f.plan.WaitTimeoutSeconds = 30
		f.run(t, f.serviceRestart())
		if elapsed := time.Since(startedAt); elapsed < time.Second {
			t.Fatalf("script finished after %s, before the upgrading process could exit", elapsed)
		}
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Fatal("upgrading process still running after the script finished")
		}
		if got := f.installed(t); got != "new" {
			t.Fatalf("installed = %q, want new", got)
		}
	})
}
