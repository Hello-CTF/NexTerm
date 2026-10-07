package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

type fakeVault struct {
	mu          sync.Mutex
	status      vault.Status
	initialized int
	unlocked    int
	password    string
	err         error
}

func (v *fakeVault) Status() vault.Status {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.status
}

func (v *fakeVault) InitMaster(_ context.Context, password string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.initialized++
	v.password = password
	if v.err == nil {
		v.status.Initialized = true
		v.status.Unlocked = true
	}
	return v.err
}

func (v *fakeVault) UnlockMaster(_ context.Context, password string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.unlocked++
	v.password = password
	if v.err == nil {
		v.status.Unlocked = true
	}
	return v.err
}

type fakeTokenStore struct {
	mu    sync.Mutex
	token string
}

func (s *fakeTokenStore) VerifyToken(_ context.Context, presented string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return presented != "" && presented == s.token, nil
}

func TestServerCLIEnvironmentFlagAndTokenContracts(t *testing.T) {
	environment := map[string]string{
		"NEXTERM_LISTEN":     "127.0.0.1:9000",
		"NEXTERM_DATA_DIR":   "/env/data",
		"NEXTERM_WEB_ROOT":   "/env/web",
		"NEXTERM_MASTER_KEY": "env-secret",
	}
	getenv := func(name string) string { return environment[name] }
	invocation, err := ParseCLI([]string{"--master-key=flag-secret", "serve", "--data-dir", "/flag/data", "--sync-only=false"}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Command != core.CommandServe || invocation.Options.DataDir != "/flag/data" || invocation.Options.Listen != "127.0.0.1:9000" || invocation.Options.WebRoot != "/env/web" || invocation.Options.MasterKey != "flag-secret" || invocation.Options.SyncOnly {
		t.Fatalf("invocation = %+v", invocation)
	}
	if invocation.Options.Auth != AuthOn {
		t.Fatalf("default auth mode = %q, want %q", invocation.Options.Auth, AuthOn)
	}
	invocation, err = ParseCLI([]string{"--auth=loopback"}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Options.Auth != AuthLoopback {
		t.Fatalf("auth flag = %+v", invocation)
	}
	coreInvocation, err := core.ParseCLI([]string{"--auth=loopback", "--require-vault"}, core.CommandServe, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if coreInvocation.Auth != core.AuthLoopback || !coreInvocation.RequireVault {
		t.Fatalf("core auth flags = %+v", coreInvocation)
	}
	invocation, err = ParseCLI(nil, func(string) string { return "" })
	if err != nil || invocation.Command != core.CommandServe || invocation.Options.Listen != DefaultListen || invocation.Options.DataDir == "" || invocation.Options.WebRoot == "" {
		t.Fatalf("bare invocation = %+v, %v", invocation, err)
	}
	if invocation.Options.Auth != AuthOn {
		t.Fatalf("bare auth mode = %q, want %q", invocation.Options.Auth, AuthOn)
	}
	if _, err := ParseCLI([]string{"--auth=bogus"}, getenv); err == nil {
		t.Fatal("invalid --auth value was accepted")
	}
	if _, err := ParseCLI(nil, func(name string) string {
		if name == "NEXTERM_AUTH" {
			return "bogus"
		}
		return ""
	}); err == nil {
		t.Fatal("invalid NEXTERM_AUTH was accepted")
	}
	invocation, err = ParseCLI(nil, func(name string) string {
		if name == "NEXTERM_AUTH" {
			return AuthOff
		}
		return ""
	})
	if err != nil || invocation.Options.Auth != AuthOff {
		t.Fatalf("NEXTERM_AUTH = %+v, %v", invocation, err)
	}
	if _, err := ParseCLI([]string{"desktop"}, getenv); err == nil {
		t.Fatal("server accepted desktop command")
	}
	usage := Usage("nexterm-server")
	if strings.Contains(usage, "desktop") || strings.Contains(usage, "rotate-token") || !strings.Contains(usage, "NEXTERM_MASTER_KEY") {
		t.Fatalf("server usage = %q", usage)
	}
}

func TestResolveMasterKey(t *testing.T) {
	key, err := ResolveMasterKey("env-secret", "")
	if err != nil || key != "env-secret" {
		t.Fatalf("passthrough = %q, %v", key, err)
	}
	keyFile := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyFile, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err = ResolveMasterKey("", keyFile)
	if err != nil || key != "file-secret" {
		t.Fatalf("file key = %q, %v", key, err)
	}
	if _, err := ResolveMasterKey("env-secret", keyFile); err == nil {
		t.Fatal("combined --master-key and --master-key-file were accepted")
	}
	emptyFile := filepath.Join(t.TempDir(), "empty.key")
	if err := os.WriteFile(emptyFile, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveMasterKey("", emptyFile); err == nil {
		t.Fatal("empty master key file was accepted")
	}
	if _, err := ResolveMasterKey("", filepath.Join(t.TempDir(), "missing.key")); err == nil {
		t.Fatal("missing master key file was accepted")
	}
	invocation, err := ParseCLI([]string{"--master-key-file", keyFile, "--data-dir", t.TempDir()}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Options.MasterKey != "file-secret" {
		t.Fatalf("ParseCLI master key = %q", invocation.Options.MasterKey)
	}
	if _, err := ParseCLI([]string{"--master-key=flag-secret", "--master-key-file", keyFile, "--data-dir", t.TempDir()}, func(string) string { return "" }); err == nil {
		t.Fatal("ParseCLI accepted combined key sources")
	}
}

func TestResolveDBPassword(t *testing.T) {
	password, err := ResolveDBPassword("")
	if err != nil || password != "" {
		t.Fatalf("passthrough = %q, %v", password, err)
	}
	dir := t.TempDir()
	passwordFile := filepath.Join(dir, "pg.password")
	if err := os.WriteFile(passwordFile, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	password, err = ResolveDBPassword(passwordFile)
	if err != nil || password != "file-secret" {
		t.Fatalf("file password = %q, %v", password, err)
	}
	looseFile := filepath.Join(dir, "loose.password")
	if err := os.WriteFile(looseFile, []byte("file-secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveDBPassword(looseFile); err == nil {
		t.Fatal("0644 password file was accepted")
	}
	if _, err := ResolveDBPassword(dir); err == nil {
		t.Fatal("directory password file was accepted")
	}
	emptyFile := filepath.Join(dir, "empty.password")
	if err := os.WriteFile(emptyFile, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveDBPassword(emptyFile); err == nil {
		t.Fatal("empty password file was accepted")
	}
	if _, err := ResolveDBPassword(filepath.Join(dir, "missing.password")); err == nil {
		t.Fatal("missing password file was accepted")
	}
}

func TestBootstrapVaultRequired(t *testing.T) {
	if err := BootstrapVaultRequired(context.Background(), &fakeVault{}, ""); err == nil {
		t.Fatal("missing master key was accepted")
	}
	failure := &fakeVault{err: errors.New("wrong key"), status: vault.Status{Initialized: true}}
	if err := BootstrapVaultRequired(context.Background(), failure, "wrong-password"); err == nil {
		t.Fatal("vault bootstrap failure was accepted")
	}
	fresh := &fakeVault{}
	if err := BootstrapVaultRequired(context.Background(), fresh, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if fresh.initialized != 1 || !fresh.status.Unlocked {
		t.Fatalf("fresh vault = %+v", fresh)
	}
	initialized := &fakeVault{status: vault.Status{Initialized: true}}
	if err := BootstrapVaultRequired(context.Background(), initialized, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if initialized.unlocked != 1 || !initialized.status.Unlocked {
		t.Fatalf("initialized vault = %+v", initialized)
	}
}

func TestBootstrapVaultInitializesOrUnlocks(t *testing.T) {
	fresh := &fakeVault{}
	if err := BootstrapVault(context.Background(), fresh, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if fresh.initialized != 1 || fresh.unlocked != 0 || fresh.password != "correct-password" {
		t.Fatalf("fresh vault = %+v", fresh)
	}
	initialized := &fakeVault{status: vault.Status{Initialized: true}}
	if err := BootstrapVault(context.Background(), initialized, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if initialized.initialized != 0 || initialized.unlocked != 1 {
		t.Fatalf("initialized vault = %+v", initialized)
	}
	if err := BootstrapVault(context.Background(), &fakeVault{}, "short"); err == nil {
		t.Fatal("short master key was accepted")
	}
	if err := BootstrapVault(context.Background(), nil, "correct-password"); err == nil {
		t.Fatal("missing vault was accepted")
	}
	failure := &fakeVault{err: errors.New("wrong key"), status: vault.Status{Initialized: true}}
	if err := BootstrapVault(context.Background(), failure, "wrong-password"); err == nil {
		t.Fatal("vault failure was discarded")
	}
}

func TestNonLoopbackWarningsReachLoggerAndStderr(t *testing.T) {
	for _, syncOnly := range []bool{false, true} {
		var logOutput bytes.Buffer
		var stderr bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logOutput, nil))
		core.WarnIfExposed(logger, &stderr, "0.0.0.0:8080", syncOnly)
		if !strings.Contains(logOutput.String(), "non-loopback") || !strings.Contains(stderr.String(), "WARNING") {
			t.Fatalf("syncOnly=%v log=%q stderr=%q", syncOnly, logOutput.String(), stderr.String())
		}
		if syncOnly && !strings.Contains(stderr.String(), "onlyServer") {
			t.Fatalf("onlyServer warning = %q", stderr.String())
		}
		if syncOnly && strings.Contains(stderr.String(), "完整版") {
			t.Fatalf("sync-only warning carries full-mode wording: %q", stderr.String())
		}
		if !syncOnly && !strings.Contains(stderr.String(), "完整版") {
			t.Fatalf("full-mode warning = %q", stderr.String())
		}
	}
	for _, address := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		var stderr bytes.Buffer
		core.WarnIfExposed(testLogger(), &stderr, address, false)
		if stderr.Len() != 0 {
			t.Fatalf("%s warning = %q", address, stderr.String())
		}
	}
}
