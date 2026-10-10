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

	core "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/vault"
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

func (v *fakeVault) SetAutoLock(_ context.Context, minutes uint64) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.status.AutoLockMinutes = minutes
	return v.err
}

func TestResolveMasterKey(t *testing.T) {
	key, err := ResolveMasterKey("")
	if err != nil || key != "" {
		t.Fatalf("empty = %q, %v", key, err)
	}
	keyFile := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyFile, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err = ResolveMasterKey(keyFile)
	if err != nil || key != "file-secret" {
		t.Fatalf("file key = %q, %v", key, err)
	}
	emptyFile := filepath.Join(t.TempDir(), "empty.key")
	if err := os.WriteFile(emptyFile, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveMasterKey(emptyFile); err == nil {
		t.Fatal("empty master key file was accepted")
	}
	if _, err := ResolveMasterKey(filepath.Join(t.TempDir(), "missing.key")); err == nil {
		t.Fatal("missing master key file was accepted")
	}
	invocation, err := core.ParseCLI([]string{"--master-key-file", keyFile, "--data-dir", t.TempDir()}, core.CommandServe, nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err = ResolveMasterKey(invocation.MasterKeyFile)
	if err != nil || key != "file-secret" {
		t.Fatalf("parsed master key = %q, %v", key, err)
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
	fresh := &fakeVault{status: vault.Status{AutoLockMinutes: 30}}
	if err := BootstrapVault(context.Background(), fresh, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if fresh.initialized != 1 || fresh.unlocked != 0 || fresh.password != "correct-password" || fresh.status.AutoLockMinutes != 0 {
		t.Fatalf("fresh vault = %+v", fresh)
	}
	initialized := &fakeVault{status: vault.Status{Initialized: true, AutoLockMinutes: 30}}
	if err := BootstrapVault(context.Background(), initialized, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if initialized.initialized != 0 || initialized.unlocked != 1 || initialized.status.AutoLockMinutes != 0 {
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
		core.WarnIfExposed(logger, &stderr, "0.0.0.0:8080", syncOnly, core.AuthOn)
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
		core.WarnIfExposed(testLogger(), &stderr, address, false, core.AuthOn)
		if stderr.Len() != 0 {
			t.Fatalf("%s warning = %q", address, stderr.String())
		}
	}
}
