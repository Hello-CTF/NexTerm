package privacy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// An INT8 or FP16 model installed by an earlier release stays listed and
// removable, but cannot be selected, enabled or downloaded, and an enabled
// selection reports why it no longer works without loading or hashing it.
func TestManagerRejectsPrecisionsTheEngineCannotRun(t *testing.T) {
	manager, settings, _, loads := managedFixture(t)
	legacy, files := fixtureInstallation(t, INT8)
	writeFixtureInstallation(t, manager.root, legacy, files)
	ctx := privacyAdmin(t, "alice")
	if _, err := manager.Configure(ctx, true, INT8, legacy.Revision); err == nil || !strings.Contains(err.Error(), "FP32") {
		t.Fatalf("enabled an INT8 model: %v", err)
	}
	if _, err := manager.Configure(ctx, false, FP16, legacy.Revision); err == nil {
		t.Fatal("selected an FP16 model")
	}
	if _, err := manager.StartDownload(ctx, INT8); err == nil || !strings.Contains(err.Error(), "FP32") {
		t.Fatalf("started an INT8 download: %v", err)
	}
	if err := settings.SettingSet(ctx, configKey("alice"), `{"enabled":true,"precision":"int8","revision":"`+legacy.Revision+`"}`); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Selection == nil || status.Selection.Available || !strings.Contains(status.Selection.Error, "FP32") {
		t.Fatalf("selection = %+v", status.Selection)
	}
	if !slices.ContainsFunc(status.Installed, func(i Installation) bool { return i.Precision == INT8 }) {
		t.Fatalf("installed INT8 model not listed: %+v", status.Installed)
	}
	if err := protectOnce(t, manager, ctx, "secret"); err == nil || !strings.Contains(err.Error(), "FP32") {
		t.Fatalf("protected with an INT8 selection: %v", err)
	}
	if loads.Load() != 0 {
		t.Fatal("an INT8 model reached the engine loader")
	}
	if _, err := manager.Configure(ctx, false, INT8, legacy.Revision); err != nil {
		t.Fatalf("could not disable the INT8 selection: %v", err)
	}
	if _, err := manager.Remove(ctx, legacy.Revision, INT8); err != nil {
		t.Fatalf("could not remove the INT8 model: %v", err)
	}
}

func TestManagerReportsBrokenEnabledSelection(t *testing.T) {
	manager, _, installation, _ := managedFixture(t)
	ctx := privacyOwner(t, "alice", "conversation")
	if _, err := manager.Configure(ctx, true, FP32, installation.Revision); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Selection == nil || !status.Selection.Available {
		t.Fatalf("healthy selection = %+v", status.Selection)
	}
	if err := os.Remove(filepath.Join(manager.modelDir(status.Config), modelFileName(FP32))); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Config.Enabled || status.Selection == nil || status.Selection.Available || status.Selection.Error == "" {
		t.Fatalf("broken selection hidden: %+v", status)
	}
	if err := os.RemoveAll(manager.modelDir(status.Config)); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Config.Revision != installation.Revision || len(status.Installed) != 0 || status.Selection.Available {
		t.Fatalf("missing selection not kept visible: %+v", status)
	}
}

func TestManagerRemoveProtectsActiveSelectionAndRequiresAdmin(t *testing.T) {
	manager, _, installation, _ := managedFixture(t)
	admin := privacyAdmin(t, "alice")
	if _, err := manager.Remove(privacyOwner(t, "bob", "conversation"), installation.Revision, FP32); err == nil {
		t.Fatal("non-admin removed a shared model")
	}
	if _, err := manager.Configure(admin, true, FP32, installation.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Remove(admin, installation.Revision, FP32); err == nil {
		t.Fatal("removed the model protection is using")
	}
	if _, err := manager.Configure(admin, false, FP32, installation.Revision); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Remove(admin, installation.Revision, FP32)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Installed) != 0 || status.DiskUsage != 0 || status.Config.Revision != "" {
		t.Fatalf("removal incomplete: %+v", status)
	}
}

func TestManagerServerAccountsCannotManageModels(t *testing.T) {
	manager, _, _, _ := managedFixture(t)
	ctx := privacyOwner(t, "bob", "conversation")
	status, err := manager.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.CanManage {
		t.Fatal("server account allowed to manage models")
	}
	for name, run := range map[string]func() error{
		"download": func() error { _, err := manager.StartDownload(ctx, FP32); return err },
		"check":    func() error { _, err := manager.CheckUpdates(ctx); return err },
	} {
		if err := run(); err == nil {
			t.Fatalf("%s allowed for non-admin", name)
		}
	}
	admin, err := manager.Status(privacyAdmin(t, "alice"))
	if err != nil || !admin.CanManage {
		t.Fatalf("admin status = %+v, %v", admin, err)
	}
}

type blockingEngine struct {
	fixtureEngine
	release chan struct{}
	failed  atomic.Bool
}

func (e *blockingEngine) Detect(ctx context.Context, text string) ([]Span, error) {
	if text == "fail" {
		e.failed.Store(true)
		return nil, errors.New("native failure")
	}
	select {
	case <-e.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return e.fixtureEngine.Detect(ctx, text)
}

func TestManagerConfigureLoadDoesNotBlockStatusOrRequests(t *testing.T) {
	settings := newPrivacySettings()
	root := t.TempDir()
	installation, files := fixtureInstallation(t, FP32)
	writeFixtureInstallation(t, root, installation, files)
	loading := make(chan struct{})
	unblock := make(chan struct{})
	manager := NewManager(settings, root, false, func(ctx context.Context, _ string, _ Installation) (Engine, error) {
		close(loading)
		<-unblock
		return &fixtureEngine{}, nil
	})
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	ctx := privacyOwner(t, "alice", "conversation")
	done := make(chan error, 1)
	go func() { _, err := manager.Configure(ctx, true, FP32, installation.Revision); done <- err }()
	<-loading
	status, err := manager.Status(ctx)
	if err != nil || status.Config.Enabled {
		t.Fatalf("status during load = %+v, %v", status, err)
	}
	if exchange, err := manager.Exchange(ctx); exchange != nil || err != nil {
		t.Fatalf("exchange during load = %v, %v", exchange, err)
	}
	close(unblock)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if status, _ := manager.Status(ctx); !status.Config.Enabled {
		t.Fatal("configure did not save after load")
	}
}

func TestManagerDetectHonorsContextAndReloadsAfterNativeFailure(t *testing.T) {
	settings := newPrivacySettings()
	root := t.TempDir()
	installation, files := fixtureInstallation(t, FP32)
	writeFixtureInstallation(t, root, installation, files)
	var loads atomic.Int64
	engines := make(chan *blockingEngine, 4)
	manager := NewManager(settings, root, false, func(context.Context, string, Installation) (Engine, error) {
		loads.Add(1)
		engine := &blockingEngine{release: make(chan struct{})}
		engines <- engine
		return engine, nil
	})
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	ctx := privacyOwner(t, "alice", "conversation")
	if _, err := manager.Configure(ctx, true, FP32, installation.Revision); err != nil {
		t.Fatal(err)
	}
	first := <-engines
	detector := managedDetector{manager: manager, config: Config{Enabled: true, Precision: FP32, Revision: installation.Revision}}
	busy := make(chan error, 1)
	go func() { _, err := detector.Detect(ctx, "secret"); busy <- err }()
	// Wait until the first request holds the engine.
	deadline := time.After(5 * time.Second)
	for len(manager.engineSlot) == 0 {
		select {
		case <-deadline:
			t.Fatal("first request did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	waiting, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := detector.Detect(waiting, "secret"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued request ignored its context: %v", err)
	}
	close(first.release)
	if err := <-busy; err != nil {
		t.Fatal(err)
	}
	if _, err := detector.Detect(ctx, "fail"); err == nil {
		t.Fatal("native failure hidden")
	}
	if !first.closed.Load() {
		t.Fatal("failed engine was not closed")
	}
	second := make(chan error, 1)
	go func() { _, err := detector.Detect(ctx, "secret"); second <- err }()
	next := <-engines
	close(next.release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 2 {
		t.Fatalf("loads = %d", loads.Load())
	}
}

func TestManagerCancellationNoticeClearsOnNextAction(t *testing.T) {
	manager, _, installation, _ := managedFixture(t)
	ctx := privacyAdmin(t, "alice")
	manager.mu.Lock()
	manager.downloads["alice"] = DownloadState{Precision: FP32, Error: "下载已取消", Canceled: true}
	manager.mu.Unlock()
	status, err := manager.Configure(ctx, false, FP32, installation.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if status.Download.Canceled || status.Download.Error != "" {
		t.Fatalf("cancellation notice kept: %+v", status.Download)
	}
}

func TestManagerStartSweepsStaleDirectoriesAndWarmsEngine(t *testing.T) {
	settings := newPrivacySettings()
	root := t.TempDir()
	installation, files := fixtureInstallation(t, FP32)
	writeFixtureInstallation(t, root, installation, files)
	for _, name := range []string{".download-123", ".replaced-456"} {
		if err := os.MkdirAll(filepath.Join(root, name, "partial"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var loads atomic.Int64
	loader := func(context.Context, string, Installation) (Engine, error) {
		loads.Add(1)
		return &fixtureEngine{}, nil
	}
	first := NewManager(settings, root, true, loader)
	if _, err := first.Configure(t.Context(), true, FP32, installation.Revision); err != nil {
		t.Fatal(err)
	}
	if err := first.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(settings, root, true, loader)
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if err := manager.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager.workers.Wait()
	if loads.Load() != 2 {
		t.Fatalf("startup did not warm the engine: loads=%d", loads.Load())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("stale directory kept: %s", entry.Name())
		}
	}
}

func TestCatalogStalledTransferFailsAndShortCircuitsInstalledModel(t *testing.T) {
	installation, files := fixtureInstallation(t, FP32)
	var fileRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileRequests.Add(1)
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	catalog := &Catalog{baseURL: server.URL, client: server.Client(), idleTimeout: 50 * time.Millisecond}
	if _, err := catalog.Download(t.Context(), t.TempDir(), installation, nil, nil); !errors.Is(err, ErrStalled) {
		t.Fatalf("stalled transfer error = %v", err)
	}
	root := t.TempDir()
	writeFixtureInstallation(t, root, installation, files)
	fileRequests.Store(0)
	if _, err := catalog.Download(t.Context(), root, installation, nil, nil); err != nil {
		t.Fatal(err)
	}
	if fileRequests.Load() != 0 {
		t.Fatal("installed model was downloaded again")
	}
}

func TestDescribeDownloadErrorUsesChineseSummaries(t *testing.T) {
	for _, test := range []struct {
		err     error
		summary string
	}{
		{fmt.Errorf("write: %w", syscall.ENOSPC), "磁盘空间不足"},
		{fmt.Errorf("%w: model.onnx", ErrIntegrity), "校验失败"},
		{&HTTPStatusError{Code: 503}, "HTTP 503"},
		{ErrStalled, "没有进度"},
		{&netError{}, "无法连接"},
	} {
		summary, detail := describeDownloadError(test.err)
		if !strings.Contains(summary, test.summary) || detail == "" {
			t.Fatalf("%v => %q / %q", test.err, summary, detail)
		}
	}
}

type netError struct{}

func (*netError) Error() string   { return "dial tcp: connection refused" }
func (*netError) Timeout() bool   { return false }
func (*netError) Temporary() bool { return false }
