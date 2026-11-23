package privacy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// multiModelFixture installs three FP32 revisions in a server-mode manager.
func multiModelFixture(t *testing.T) (*Manager, [3]string, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	settings := newPrivacySettings()
	root := t.TempDir()
	revisions := [3]string{strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)}
	for _, revision := range revisions {
		fixture, files := fixtureInstallation(t, FP32)
		fixture.Revision = revision
		writeFixtureInstallation(t, root, fixture, files)
	}
	loads, closes := &atomic.Int64{}, &atomic.Int64{}
	manager := NewManager(settings, root, false, func(context.Context, string, Installation) (Engine, error) {
		loads.Add(1)
		return &countingEngine{closes: closes}, nil
	})
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	return manager, revisions, loads, closes
}

type countingEngine struct {
	fixtureEngine
	closes *atomic.Int64
}

func (e *countingEngine) Close() error { e.closes.Add(1); return e.fixtureEngine.Close() }

func protectOnce(t *testing.T, manager *Manager, ctx context.Context, text string) error {
	t.Helper()
	exchange, err := manager.Exchange(ctx)
	if err != nil {
		return err
	}
	_, err = exchange.Protect(ctx, text)
	return err
}

func TestManagerKeepsEnginesForAlternatingAccountModels(t *testing.T) {
	manager, revisions, loads, closes := multiModelFixture(t)
	alice := privacyOwner(t, "alice", "conversation")
	bob := privacyOwner(t, "bob", "conversation")
	if _, err := manager.Configure(alice, true, FP32, revisions[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Configure(bob, true, FP32, revisions[1]); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		for _, ctx := range []context.Context{alice, bob} {
			if err := protectOnce(t, manager, ctx, fmt.Sprintf("secret %d", i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if loads.Load() != 2 || closes.Load() != 0 {
		t.Fatalf("alternation reloaded engines: loads=%d closes=%d", loads.Load(), closes.Load())
	}
	// Another account enabling a model already loaded must not drop either engine.
	if _, err := manager.Configure(privacyOwner(t, "carol", "conversation"), true, FP32, revisions[0]); err != nil {
		t.Fatal(err)
	}
	if err := protectOnce(t, manager, bob, "secret again"); err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 2 || closes.Load() != 0 {
		t.Fatalf("configure dropped a shared engine: loads=%d closes=%d", loads.Load(), closes.Load())
	}
	// A third model evicts only the least recently used engine.
	if _, err := manager.Configure(privacyOwner(t, "dave", "conversation"), true, FP32, revisions[2]); err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 3 || closes.Load() != 1 {
		t.Fatalf("eviction: loads=%d closes=%d", loads.Load(), closes.Load())
	}
	if err := protectOnce(t, manager, bob, "secret still cached"); err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 3 {
		t.Fatalf("recently used engine was evicted: loads=%d", loads.Load())
	}
}

func TestManagerReusesVerificationUntilFilesChangeOrDirectoryIsReleased(t *testing.T) {
	manager, revisions, _, _ := multiModelFixture(t)
	alice := privacyOwner(t, "alice", "conversation")
	if _, err := manager.Configure(alice, true, FP32, revisions[0]); err != nil {
		t.Fatal(err)
	}
	directory := manager.modelDir(Config{Revision: revisions[0], Precision: FP32})
	path := filepath.Join(directory, "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	evict := func() {
		t.Helper()
		for _, revision := range revisions[1:] {
			if _, err := manager.Configure(privacyOwner(t, "other-"+revision[:1], "conversation"), true, FP32, revision); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Same size and timestamp: the remembered verification still applies, so
	// the edit is not hashed again (a full hash would reject it).
	if err := os.WriteFile(path, []byte(strings.Repeat("x", len(original))), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	evict()
	if err := protectOnce(t, manager, alice, "secret reload"); err != nil {
		t.Fatalf("unchanged fingerprint was verified again: %v", err)
	}
	// Releasing the directory forgets the verification.
	if err := manager.releaseDirectory(t.Context(), directory); err != nil {
		t.Fatal(err)
	}
	if err := protectOnce(t, manager, alice, "secret after release"); err == nil {
		t.Fatal("released directory skipped verification")
	}
	// A changed timestamp forces verification too.
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := protectOnce(t, manager, alice, "secret restored"); err != nil {
		t.Fatal(err)
	}
	later := info.ModTime().Add(time.Hour)
	if err := os.WriteFile(path, []byte(strings.Repeat("y", len(original))), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	evict()
	if err := protectOnce(t, manager, alice, "secret tampered"); err == nil {
		t.Fatal("changed model loaded without verification")
	}
}

func TestManagerRemoveRefusesModelEnabledByOtherAccounts(t *testing.T) {
	manager, revisions, _, _ := multiModelFixture(t)
	for _, id := range []string{"bob", "carol"} {
		if _, err := manager.Configure(privacyOwner(t, id, "conversation"), true, FP32, revisions[1]); err != nil {
			t.Fatal(err)
		}
	}
	admin := privacyAdmin(t, "alice")
	_, err := manager.Remove(admin, revisions[1], FP32)
	if err == nil || !strings.Contains(err.Error(), "2 个账号") {
		t.Fatalf("remove of a model other accounts use: %v", err)
	}
	if _, err := manager.Remove(admin, revisions[0], FP32); err != nil {
		t.Fatalf("remove of an unused model: %v", err)
	}
}

func TestUnscopedExchangeDoesNotFillSharedDetectionCache(t *testing.T) {
	manager, revisions, _, _ := multiModelFixture(t)
	ctx := ipc.WithUserID(t.Context(), "alice")
	if _, err := manager.Configure(ctx, true, FP32, revisions[0]); err != nil {
		t.Fatal(err)
	}
	cached := func() int {
		sharedDetections.mu.Lock()
		defer sharedDetections.mu.Unlock()
		return sharedDetections.order.Len()
	}
	before := cached()
	if err := protectOnce(t, manager, ctx, "secret without a conversation"); err != nil {
		t.Fatal(err)
	}
	if after := cached(); after != before {
		t.Fatalf("unscoped request filled the shared cache: %d -> %d", before, after)
	}
	if err := protectOnce(t, manager, privacyOwner(t, "alice", "conversation"), "secret with a conversation"); err != nil {
		t.Fatal(err)
	}
	if cached() == before {
		t.Fatal("scoped request did not use the shared cache")
	}
}

func TestModelFailuresAreClassified(t *testing.T) {
	manager, revisions, _, _ := multiModelFixture(t)
	ctx := privacyOwner(t, "alice", "conversation")
	if _, err := manager.Configure(ctx, true, FP32, revisions[0]); err != nil {
		t.Fatal(err)
	}
	directory := manager.modelDir(Config{Revision: revisions[0], Precision: FP32})
	if err := manager.releaseDirectory(t.Context(), directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := protectOnce(t, manager, ctx, "secret"); !IsModelError(err) {
		t.Fatalf("missing model file error = %v", err)
	}
	settings := manager.settings.(*memoryPrivacySettings)
	settings.protector.locked.Store(true)
	if err := protectOnce(t, manager, ctx, "secret"); err == nil || IsModelError(err) {
		t.Fatalf("locked vault error = %v", err)
	}
}
