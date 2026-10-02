package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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
	return v.err
}

type fakeTokenStore struct {
	mu         sync.Mutex
	token      string
	syncCalls  int
	rotateCall int
}

func (s *fakeTokenStore) SyncToken(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncCalls++
	if s.token == "" {
		s.token = "generated"
	}
	return s.token, nil
}

func (s *fakeTokenStore) RotateSyncToken(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotateCall++
	s.token = "rotated"
	return s.token, nil
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
	invocation, err := ParseCLI([]string{"--master-key=flag-secret", "rotate-token", "--data-dir", "/flag/data", "--sync-only=false"}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Command != core.CommandRotateToken || invocation.Options.DataDir != "/flag/data" || invocation.Options.Listen != "127.0.0.1:9000" || invocation.Options.WebRoot != "/env/web" || invocation.Options.MasterKey != "flag-secret" || invocation.Options.SyncOnly {
		t.Fatalf("invocation = %+v", invocation)
	}
	invocation, err = ParseCLI(nil, func(string) string { return "" })
	if err != nil || invocation.Command != core.CommandServe || invocation.Options.Listen != DefaultListen || invocation.Options.DataDir == "" || invocation.Options.WebRoot == "" {
		t.Fatalf("bare invocation = %+v, %v", invocation, err)
	}
	if _, err := ParseCLI([]string{"desktop"}, getenv); err == nil {
		t.Fatal("server accepted desktop command")
	}

	store := &fakeTokenStore{}
	var stdout bytes.Buffer
	if err := RunTokenCommand(context.Background(), core.CommandToken, store, &stdout); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "generated\n" {
		t.Fatalf("token stdout = %q", stdout.String())
	}
	stdout.Reset()
	if err := RunTokenCommand(context.Background(), core.CommandRotateToken, store, &stdout); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "rotated\n" || store.rotateCall != 1 {
		t.Fatalf("rotate stdout = %q calls=%d", stdout.String(), store.rotateCall)
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
		warnIfExposed(logger, &stderr, "0.0.0.0:8080", syncOnly)
		if !strings.Contains(logOutput.String(), "non-loopback") || !strings.Contains(stderr.String(), "WARNING") {
			t.Fatalf("syncOnly=%v log=%q stderr=%q", syncOnly, logOutput.String(), stderr.String())
		}
		if syncOnly && !strings.Contains(stderr.String(), "onlyServer") {
			t.Fatalf("onlyServer warning = %q", stderr.String())
		}
	}
	for _, address := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		var stderr bytes.Buffer
		warnIfExposed(testLogger(), &stderr, address, false)
		if stderr.Len() != 0 {
			t.Fatalf("%s warning = %q", address, stderr.String())
		}
	}
}
