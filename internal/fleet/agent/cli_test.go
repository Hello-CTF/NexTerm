package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func stubCLIManager(t *testing.T) {
	t.Helper()
	restore := cliNewManager
	cliNewManager = func(func(string) string) ServiceManager { return &fakeManager{} }
	t.Cleanup(func() { cliNewManager = restore })
}

func TestCLIEnrollStatusAndRunGuards(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission assertions")
	}
	stubCLIManager(t)
	server := newFakeFleetServer(t)
	dataDir := t.TempDir()
	getenv := func(key string) string {
		if key == "NEXTERM_DATA_DIR" {
			return dataDir
		}
		return ""
	}
	var stdout, stderr bytes.Buffer

	code := RunCLI([]string{"status"}, getenv, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("status on unenrolled device = %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "未注册") {
		t.Fatalf("status output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = RunCLI([]string{"run"}, getenv, &stdout, &stderr)
	if code != ExitError || !strings.Contains(stderr.String(), "未注册") {
		t.Fatalf("run unenrolled = %d, stderr %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = RunCLI([]string{"enroll", "--server", server.server.URL, "--code", "enroll-code", "--name", "cli-device"}, getenv, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("enroll = %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "01J5DEVICE0000000000000000") {
		t.Fatalf("enroll output = %q", stdout.String())
	}
	store := StoreAt(dataDir)
	config, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.Secret != "device-secret" || config.Name != "cli-device" || config.Platform != runtime.GOOS {
		t.Fatalf("enrolled config = %+v", config)
	}
	info, err := os.Stat(filepath.Join(dataDir, "fleet", "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, want 0600", info.Mode().Perm())
	}

	stdout.Reset()
	stderr.Reset()
	code = RunCLI([]string{"status"}, getenv, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("status = %d, stderr %s", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "01J5DEVICE0000000000000000") {
		t.Fatalf("status output missing device id: %q", output)
	}
	if strings.Contains(output, "device-secret") {
		t.Fatal("status output must not leak the device secret")
	}
}

func TestCLIUsageErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := RunCLI(nil, nil, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("no args = %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunCLI([]string{"bogus"}, nil, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("unknown command = %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunCLI([]string{"enroll", "--server", "https://x.example.com"}, nil, &stdout, &stderr); code != ExitUsage {
		t.Fatal("enroll without --code must be usage error")
	}
	stdout.Reset()
	stderr.Reset()
	noEnv := func(string) string { return "" }
	if code := RunCLI([]string{"status"}, noEnv, &stdout, &stderr); code != ExitUsage {
		t.Fatal("status without data dir must be usage error")
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunCLI([]string{"run", "-data-dir", t.TempDir(), "-pprof-addr", "192.168.1.10:6060"}, noEnv, &stdout, &stderr); code != ExitUsage {
		t.Fatal("run with a non-loopback pprof address must be usage error")
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunCLI([]string{"enroll", "--insecure=https://user:synthetic-password@example.invalid"}, noEnv, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("invalid boolean flag = %d", code)
	}
	if strings.Contains(stderr.String(), "synthetic-password") || !strings.Contains(stderr.String(), "user:***@example.invalid") {
		t.Fatalf("flag parser exposed URL credentials: %q", stderr.String())
	}
}

func TestCLIConfigErrorsRedactURLCredentials(t *testing.T) {
	dataDir := t.TempDir()
	store := StoreAt(dataDir)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	config := Config{
		Version: configVersion, DeviceID: "synthetic-device", Secret: "synthetic-device-secret",
		BaseURLs: []BaseURLEntry{{URL: "http://user:synthetic-password@example.invalid"}},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := RunCLI([]string{"run", "--data-dir", dataDir}, nil, &stdout, &stderr); code != ExitError {
		t.Fatalf("run with invalid URL = %d", code)
	}
	if strings.Contains(stderr.String(), "synthetic-password") || !strings.Contains(stderr.String(), "user:***@example.invalid") {
		t.Fatalf("run exposed URL credentials: %q", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunCLI([]string{"status", "--data-dir", dataDir}, nil, &stdout, &stderr); code != ExitOK {
		t.Fatalf("status with invalid URL = %d", code)
	}
	if strings.Contains(stdout.String(), "synthetic-password") || !strings.Contains(stdout.String(), "user:***@example.invalid") {
		t.Fatalf("status exposed URL credentials: %q", stdout.String())
	}
	config.BaseURLs = []BaseURLEntry{{URL: "https://example.invalid"}}
	config.CurrentURL = "https://user:synthetic-password@example.invalid"
	encoded, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunCLI([]string{"status", "--data-dir", dataDir}, nil, &stdout, &stderr); code != ExitOK {
		t.Fatalf("status with configured URL = %d", code)
	}
	if strings.Contains(stdout.String(), "synthetic-password") || !strings.Contains(stdout.String(), "user:***@example.invalid") {
		t.Fatalf("status exposed current URL credentials: %q", stdout.String())
	}
}

func TestCLIRunLockBusyExitsCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix flock")
	}
	server := newFakeFleetServer(t)
	dataDir := t.TempDir()
	getenv := func(key string) string {
		if key == "NEXTERM_DATA_DIR" {
			return dataDir
		}
		return ""
	}
	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"enroll", "--server", server.server.URL, "--code", "c"}, getenv, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("enroll = %d, %s", code, stderr.String())
	}
	unlock, err := acquireLock(filepath.Join(dataDir, "fleet", "agent.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	stdout.Reset()
	stderr.Reset()
	code = RunCLI([]string{"run"}, getenv, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("run with busy lock = %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "已有另一个设备代理实例正在运行") {
		t.Fatalf("run output = %q", stdout.String())
	}
}

func TestCLIRunRevokedExitCode(t *testing.T) {
	server := newFakeFleetServer(t)
	server.forbidSync()
	dataDir := t.TempDir()
	getenv := func(key string) string {
		if key == "NEXTERM_DATA_DIR" {
			return dataDir
		}
		return ""
	}
	restoreFactory := cliRuntimeFactory
	cliRuntimeFactory = func(store *Store, dataDir string, prober *Prober, manager ServiceManager) (*Runtime, error) {
		slog.Warn("agent logger redaction", "token", "synthetic-secret")
		return New(Options{
			Store: store, DataDir: dataDir, Prober: prober, Manager: manager,
			Collector:    fakeCollector{},
			EnsureHelper: func(context.Context) error { return nil },
			Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}
	t.Cleanup(func() { cliRuntimeFactory = restoreFactory })
	stubCLIManager(t)
	var stdout, stderr bytes.Buffer
	if code := RunCLI([]string{"enroll", "--server", server.server.URL, "--code", "c"}, getenv, &stdout, &stderr); code != ExitOK {
		t.Fatalf("enroll = %d, %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := RunCLI([]string{"run"}, getenv, &stdout, &stderr)
	if code != ExitRevoked {
		t.Fatalf("run revoked = %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "吊销") {
		t.Fatalf("run stderr = %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "synthetic-secret") || !strings.Contains(stderr.String(), "token=***") {
		t.Fatalf("agent logger did not redact structured credential: %q", stderr.String())
	}
}

func TestCLIEnrollSurfacesServerRejection(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	getenv := func(string) string { return t.TempDir() }
	code := RunCLI([]string{"enroll", "--server", "http://127.0.0.1:1", "--code", "c"}, getenv, stdout, stderr)
	if code != ExitError {
		t.Fatalf("enroll against dead server = %d", code)
	}
	if !strings.Contains(stderr.String(), "注册失败") {
		t.Fatalf("enroll stderr = %q", stderr.String())
	}
}

func stubCLIRuntimeWithUpgrader(t *testing.T, upgrader *Upgrader) {
	t.Helper()
	restoreFactory := cliRuntimeFactory
	cliRuntimeFactory = func(store *Store, dataDir string, prober *Prober, manager ServiceManager) (*Runtime, error) {
		return New(Options{
			Store: store, DataDir: dataDir, Prober: prober, Manager: manager,
			Collector:    fakeCollector{},
			EnsureHelper: func(context.Context) error { return nil },
			Upgrader:     upgrader,
			Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}
	t.Cleanup(func() { cliRuntimeFactory = restoreFactory })
	stubCLIManager(t)
}

func TestCLIRunSelfUpgradeStagedExitsOK(t *testing.T) {
	version := "1.2.3"
	server := newFakeFleetServer(t)
	server.setWSControl(mismatchControl(version))
	asset := releaseAssetName(version, runtime.GOOS, runtime.GOARCH)
	archive := buildReleaseArchive(t, runtime.GOOS, []byte("new-agent"))
	release := newReleaseServer(t, map[string][]byte{
		"/v" + version + "/" + asset:             archive,
		"/v" + version + "/" + checksumsFileName: []byte(fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)),
	})
	target := filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS))
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var replaced []stagedReplace
	stubCLIRuntimeWithUpgrader(t, &Upgrader{
		BaseURL: release.URL, Executable: target,
		MaxAttempts: 3, Backoff: time.Millisecond,
		sleep: instantSleep, scheduleReplace: captureReplace(&replaced),
	})
	dataDir := t.TempDir()
	getenv := func(key string) string {
		if key == "NEXTERM_DATA_DIR" {
			return dataDir
		}
		return ""
	}
	var stdout, stderr bytes.Buffer
	if code := RunCLI([]string{"enroll", "--server", server.server.URL, "--code", "c"}, getenv, &stdout, &stderr); code != ExitOK {
		t.Fatalf("enroll = %d, %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunCLI([]string{"run"}, getenv, &stdout, &stderr); code != ExitOK {
		t.Fatalf("run with staged self-upgrade = %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "自动完成替换") {
		t.Fatalf("run stdout = %q", stdout.String())
	}
	if len(replaced) != 1 {
		t.Fatalf("scheduleReplace calls = %+v", replaced)
	}
}

func TestCLIRunProtocolMismatchExitCode(t *testing.T) {
	server := newFakeFleetServer(t)
	server.setWSControl(mismatchControl("9.9.9"))
	release := newReleaseServer(t, map[string][]byte{})
	stubCLIRuntimeWithUpgrader(t, &Upgrader{
		BaseURL: release.URL, Executable: filepath.Join(t.TempDir(), "nexterm-agent"),
		MaxAttempts: 3, Backoff: time.Millisecond,
		sleep: instantSleep, scheduleReplace: func(string, string) error { return nil },
	})
	dataDir := t.TempDir()
	getenv := func(key string) string {
		if key == "NEXTERM_DATA_DIR" {
			return dataDir
		}
		return ""
	}
	var stdout, stderr bytes.Buffer
	if code := RunCLI([]string{"enroll", "--server", server.server.URL, "--code", "c"}, getenv, &stdout, &stderr); code != ExitOK {
		t.Fatalf("enroll = %d, %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunCLI([]string{"run"}, getenv, &stdout, &stderr); code != ExitProtocol {
		t.Fatalf("run with failed self-upgrade = %d, want ExitProtocol", code)
	}
	if !strings.Contains(stderr.String(), "install-device.sh") {
		t.Fatalf("run stderr = %q, want manual reinstall guidance", stderr.String())
	}
}
