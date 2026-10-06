package agent

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
}

func TestCLIRunLockBusyExitsCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix flock")
	}
	server := newFakeFleetServer(t)
	dataDir := t.TempDir()
	getenv := func(string) string { return dataDir }
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
	if !strings.Contains(stdout.String(), "另一个 agent 实例正在运行") {
		t.Fatalf("run output = %q", stdout.String())
	}
}

func TestCLIRunRevokedExitCode(t *testing.T) {
	server := newFakeFleetServer(t)
	server.forbidSync()
	dataDir := t.TempDir()
	getenv := func(string) string { return dataDir }
	restoreFactory := cliRuntimeFactory
	cliRuntimeFactory = func(store *Store, dataDir string, prober *Prober, manager ServiceManager) (*Runtime, error) {
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
