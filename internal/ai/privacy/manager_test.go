package privacy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type memoryPrivacySettings struct {
	mu        sync.Mutex
	values    map[string]string
	protector *fixtureProtector
}

func (s *memoryPrivacySettings) SettingGet(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	return v, ok, nil
}
func (s *memoryPrivacySettings) SettingSet(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}
func (s *memoryPrivacySettings) SettingListPrefix(_ context.Context, prefix string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := map[string]string{}
	for key, value := range s.values {
		if strings.HasPrefix(key, prefix) {
			values[key] = value
		}
	}
	return values, nil
}
func (s *memoryPrivacySettings) SecretProtector() store.SecretProtector { return s.protector }

type fixtureProtector struct {
	mu     sync.Mutex
	values map[string]string
	locked atomic.Bool
}

func (p *fixtureProtector) EncryptSecret(_ context.Context, value string) (string, error) {
	if p.locked.Load() {
		return "", errors.New("locked")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := fmt.Sprintf("encrypted-fixture-%d", len(p.values))
	p.values[key] = value
	return key, nil
}
func (p *fixtureProtector) DecryptSecret(_ context.Context, key string) (string, error) {
	if p.locked.Load() {
		return "", errors.New("locked")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.values[key]
	if !ok {
		return "", errors.New("unknown key")
	}
	return v, nil
}
func newPrivacySettings() *memoryPrivacySettings {
	return &memoryPrivacySettings{values: map[string]string{}, protector: &fixtureProtector{values: map[string]string{}}}
}

type fixtureEngine struct {
	closed atomic.Bool
	calls  atomic.Int64
}

func (e *fixtureEngine) Detect(_ context.Context, text string) ([]Span, error) {
	if e.closed.Load() {
		return nil, errors.New("engine closed")
	}
	e.calls.Add(1)
	start := strings.Index(text, "secret")
	if start < 0 {
		return nil, nil
	}
	return []Span{{Start: start, End: start + len("secret"), Label: string(KindCommonSecret)}}, nil
}
func (e *fixtureEngine) Close() error { e.closed.Store(true); return nil }

func managedFixture(t *testing.T) (*Manager, *memoryPrivacySettings, Installation, *atomic.Int64) {
	t.Helper()
	settings := newPrivacySettings()
	root := t.TempDir()
	installation, files := fixtureInstallation(t, INT8)
	writeFixtureInstallation(t, root, installation, files)
	loads := &atomic.Int64{}
	manager := NewManager(settings, root, false, func(context.Context, string, Installation) (Engine, error) {
		loads.Add(1)
		return &fixtureEngine{}, nil
	})
	manager.runtime = nil
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	return manager, settings, installation, loads
}
func privacyOwner(t *testing.T, id, conversation string) context.Context {
	t.Helper()
	return WithConversation(ipc.WithUserID(t.Context(), id), conversation)
}
func privacyAdmin(t *testing.T, id string) context.Context {
	t.Helper()
	return ipc.WithRole(privacyOwner(t, id, "conversation"), "superadmin")
}

func TestManagerDefaultDisabledDoesNotReadModelsOrVault(t *testing.T) {
	settings := newPrivacySettings()
	settings.protector.locked.Store(true)
	root := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(root, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(settings, root, false, func(context.Context, string, Installation) (Engine, error) {
		t.Error("disabled manager loaded model")
		return nil, errors.New("unexpected load")
	})
	exchange, err := manager.Exchange(privacyOwner(t, "alice", "conversation"))
	if err != nil || exchange != nil {
		t.Fatalf("disabled exchange=%v err=%v", exchange, err)
	}
	if _, err := manager.Exchange(t.Context()); err == nil {
		t.Fatal("unauthenticated account-mode request accepted")
	}
}

func TestManagerStableAcrossRestartAndIsolatedAcrossOwners(t *testing.T) {
	manager, settings, installation, _ := managedFixture(t)
	alice := privacyOwner(t, "alice", "same-conversation")
	bob := privacyOwner(t, "bob", "same-conversation")
	for _, ctx := range []context.Context{alice, bob} {
		if _, err := manager.Configure(ctx, true, INT8, installation.Revision); err != nil {
			t.Fatal(err)
		}
	}
	protect := func(m *Manager, ctx context.Context) string {
		t.Helper()
		exchange, err := m.Exchange(ctx)
		if err != nil {
			t.Fatal(err)
		}
		text, err := exchange.Protect(ctx, "secret")
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	original := protect(manager, alice)
	if original == "secret" || protect(manager, alice) != original {
		t.Fatal("cross-turn placeholder changed")
	}
	if protect(manager, bob) == original {
		t.Fatal("cross-user placeholder was shared")
	}
	if protect(manager, privacyOwner(t, "alice", "other-conversation")) == original {
		t.Fatal("cross-conversation placeholder was shared")
	}
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted := NewManager(settings, manager.root, false, func(context.Context, string, Installation) (Engine, error) { return &fixtureEngine{}, nil })
	defer restarted.Shutdown(context.Background())
	if protect(restarted, alice) != original {
		t.Fatal("restart changed the stable placeholder")
	}
	status, err := restarted.Status(alice)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Config.Enabled {
		t.Fatal("enabled state lost across restart")
	}
}

func TestManagerFailsClosedForMissingCorruptOrLockedModels(t *testing.T) {
	manager, settings, installation, loads := managedFixture(t)
	ctx := privacyOwner(t, "alice", "conversation")
	if _, err := manager.Configure(ctx, true, INT8, ""); err == nil {
		t.Fatal("enabled without model")
	}
	status, err := manager.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Config.Enabled {
		t.Fatal("failed activation changed opt-in")
	}
	if err := os.WriteFile(filepath.Join(manager.modelDir(Config{Revision: installation.Revision, Precision: INT8}), modelFileName(INT8)), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Configure(ctx, true, INT8, installation.Revision); err == nil {
		t.Fatal("corrupt model enabled")
	}
	if loads.Load() != 0 {
		t.Fatal("corrupt file reached engine loader")
	}
	_, files := fixtureInstallation(t, INT8)
	writeFixtureInstallation(t, manager.root, installation, files)
	if _, err := manager.Configure(ctx, true, INT8, installation.Revision); err != nil {
		t.Fatal(err)
	}
	settings.protector.locked.Store(true)
	if exchange, err := manager.Exchange(ctx); err == nil || exchange != nil {
		t.Fatal("locked vault produced enabled exchange")
	}
}

func TestManagerShutdownInvalidatesPreviouslyIssuedExchange(t *testing.T) {
	manager, _, installation, loads := managedFixture(t)
	ctx := privacyOwner(t, "alice", "conversation")
	if _, err := manager.Configure(ctx, true, INT8, installation.Revision); err != nil {
		t.Fatal(err)
	}
	exchange, err := manager.Exchange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := loads.Load()
	if _, err := exchange.Protect(ctx, "secret"); err == nil {
		t.Fatal("closed manager still ran inference")
	}
	if loads.Load() != before {
		t.Fatal("closed manager reloaded an engine")
	}
	if _, err := manager.Exchange(ctx); err == nil {
		t.Fatal("closed manager issued new exchange")
	}
}

func TestManagerConcurrentRequestsKeepStableOwnerMappings(t *testing.T) {
	manager, _, installation, _ := managedFixture(t)
	ctx := privacyOwner(t, "alice", "conversation")
	if _, err := manager.Configure(ctx, true, INT8, installation.Revision); err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 16)
	failures := make(chan error, 16)
	var workers sync.WaitGroup
	for range cap(results) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			exchange, err := manager.Exchange(ctx)
			if err != nil {
				failures <- err
				return
			}
			result, err := exchange.Protect(ctx, "secret")
			if err != nil {
				failures <- err
				return
			}
			results <- result
		}()
	}
	workers.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var first string
	for result := range results {
		if first == "" {
			first = result
		}
		if result != first {
			t.Fatal("concurrent request changed placeholder")
		}
	}
}

func TestManagerDownloadDoesNotEnableProtection(t *testing.T) {
	manager, _, installation, _ := managedFixture(t)
	_, files := fixtureInstallation(t, INT8)
	manager.catalog = newCatalogFixture(t, installation, files).catalog
	ctx := privacyAdmin(t, "alice")
	if _, err := manager.StartDownload(ctx, INT8); err != nil {
		t.Fatal(err)
	}
	manager.workers.Wait()
	status, err := manager.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Download.Running || status.Download.Error != "" {
		t.Fatalf("download=%+v", status.Download)
	}
	if status.Config.Enabled {
		t.Fatal("download silently activated protection")
	}
	if status.Config.Revision != installation.Revision || status.Download.Completed == nil || status.Download.Completed.Version != installation.Version {
		t.Fatalf("first download was not selected or reported: %+v", status)
	}
	if len(status.Installed) == 0 {
		t.Fatal("download did not install a model")
	}
	bob, err := manager.Status(privacyOwner(t, "bob", "conversation"))
	if err != nil {
		t.Fatal(err)
	}
	if bob.Download.Running || bob.Download.Progress.Downloaded != 0 {
		t.Fatal("download status leaked across accounts")
	}
}

func TestManagerCanDisableAfterModelFilesDisappear(t *testing.T) {
	manager, _, installation, _ := managedFixture(t)
	ctx := privacyOwner(t, "alice", "conversation")
	if _, err := manager.Configure(ctx, true, INT8, installation.Revision); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(manager.modelDir(Config{Revision: installation.Revision, Precision: INT8})); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Configure(ctx, false, INT8, installation.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if status.Config.Enabled {
		t.Fatal("could not disable unavailable model")
	}
	if exchange, err := manager.Exchange(ctx); exchange != nil || err != nil {
		t.Fatalf("disabled exchange=%v err=%v", exchange, err)
	}
}

func TestManagerDownloadCancellationBelongsToInitiatingOwner(t *testing.T) {
	installation, files := fixtureInstallation(t, INT8)
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/refs"):
			fmt.Fprintf(w, `{"tags":[{"name":%q,"targetCommit":%q}]}`, installation.Version, installation.Revision)
		case strings.HasSuffix(r.URL.Path, "/CHECKSUMS.json"):
			_ = json.NewEncoder(w).Encode(installation.Files)
		case strings.HasSuffix(r.URL.Path, "/config.json"):
			close(started)
			<-r.Context().Done()
		default:
			name := filepath.Base(r.URL.Path)
			if data, ok := files[name]; ok {
				_, _ = w.Write(data)
			} else {
				http.NotFound(w, r)
			}
		}
	}))
	defer server.Close()
	manager := NewManager(newPrivacySettings(), t.TempDir(), false, nil)
	manager.runtime = nil
	manager.catalog = &Catalog{baseURL: server.URL, client: server.Client()}
	defer manager.Shutdown(context.Background())
	alice := privacyAdmin(t, "alice")
	bob := privacyOwner(t, "bob", "conversation")
	if _, err := manager.StartDownload(alice, INT8); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-t.Context().Done():
		t.Fatal("download did not start")
	}
	if err := manager.CancelDownload(bob); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(alice)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Download.Running {
		t.Fatal("another account canceled download")
	}
	if err := manager.CancelDownload(alice); err != nil {
		t.Fatal(err)
	}
	manager.workers.Wait()
	status, err = manager.Status(alice)
	if err != nil {
		t.Fatal(err)
	}
	if status.Download.Running || status.Download.Error == "" || !status.Download.Canceled {
		t.Fatalf("cancellation not visible: %+v", status.Download)
	}
	if status.Config.Enabled || status.Config.Revision != "" || len(status.Installed) > 0 {
		t.Fatal("canceled download activated or published a model")
	}
}
