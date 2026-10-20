//go:build darwin || linux

package production

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type forwardRuntimeFixture struct {
	db      *store.Store
	manager *session.Manager
	forward *forward.Service
	runtime *forwardRuntime
	ctx     context.Context
	asset   store.AssetRow
}

func newForwardRuntimeFixture(t *testing.T, upstream string) *forwardRuntimeFixture {
	t.Helper()
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.GORM().WithContext(t.Context()).AutoMigrate(&store.ForwardRuleRow{}); err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(context.Background(), "runtime-owner")
	asset, err := db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "runtime-host", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			return &fakeDialTransport{
				fakeSSHTransport: fakeSSHTransport{kind: asset.Kind, generation: generation},
				upstream:         upstream,
			}, nil
		}),
		IdleTimeout: -1,
	})
	t.Cleanup(func() { _ = manager.Close() })
	forwardService := forward.NewService(forward.Config{
		Provider: manager,
		SessionHost: func(ctx context.Context, sessionID string) (string, error) {
			connected, err := manager.SessionFor(ctx, sessionID)
			if err != nil {
				return "", err
			}
			return connected.Asset().ID, nil
		},
		Rules:            db,
		Policy:           forward.Policy{},
		Reconnect:        forward.ReconnectPolicy{Attempts: 1, Backoff: time.Millisecond},
		DialTimeout:      time.Second,
		HandshakeTimeout: time.Second,
		CloseTimeout:     time.Second,
	})
	t.Cleanup(func() { _ = forwardService.Close() })
	runtime := newForwardRuntime(forwardRuntimeConfig{
		Forward:  forwardService,
		Sessions: manager,
		Store:    db,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return &forwardRuntimeFixture{db: db, manager: manager, forward: forwardService, runtime: runtime, ctx: ctx, asset: asset}
}

func freeRuntimePort(t *testing.T) int32 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return int32(port)
}

func runtimeChunk(index int64, data string) chunked.Chunk {
	digest := sha256.Sum256([]byte(data))
	return chunked.Chunk{Index: index, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Data: []byte(data)}
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

type captureSlogHandler struct {
	messages []string
}

func (h *captureSlogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureSlogHandler) Handle(_ context.Context, record slog.Record) error {
	h.messages = append(h.messages, record.Message)
	return nil
}
func (h *captureSlogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureSlogHandler) WithGroup(string) slog.Handler      { return h }

func (f *forwardRuntimeFixture) saveAutoStartRule(t *testing.T, assetID string, autoStart bool) store.ForwardRuleRow {
	t.Helper()
	rule, err := f.db.ForwardRuleSave(f.ctx, store.ForwardRuleInput{
		AssetID: assetID, Name: "database", ListenPort: freeRuntimePort(t),
		TargetHost: "remote.internal", TargetPort: 4567, AutoStart: autoStart,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func TestForwardRuntimeSessionConnectedStartsAutoStartRules(t *testing.T) {
	fixture := newForwardRuntimeFixture(t, startEchoListener(t))
	rule := fixture.saveAutoStartRule(t, fixture.asset.ID, true)
	connected, err := fixture.manager.Connect(fixture.ctx, session.Asset{ID: fixture.asset.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}

	fixture.runtime.SessionConnected(fixture.ctx, connected.ID)

	runtimes := fixture.forward.ListFor(fixture.ctx)
	if len(runtimes) != 1 || runtimes[0].ID != rule.ID || runtimes[0].ListenHost != "127.0.0.1" || runtimes[0].Status != forward.StatusActive {
		t.Fatalf("auto-start runtimes = %+v", runtimes)
	}
	conn := assertForwardEcho(t, runtimes[0], "auto-start")
	_ = conn.Close()
}

func TestForwardRuntimeSessionConnectedKeepsFailuresVisible(t *testing.T) {
	fixture := newForwardRuntimeFixture(t, startEchoListener(t))
	rule := fixture.saveAutoStartRule(t, fixture.asset.ID, true)
	occupied, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(rule.ListenPort))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = occupied.Close() })
	handler := &captureSlogHandler{}
	fixture.runtime.logger = slog.New(handler)
	connected, err := fixture.manager.Connect(fixture.ctx, session.Asset{ID: fixture.asset.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}

	fixture.runtime.SessionConnected(fixture.ctx, connected.ID)

	found := false
	for _, message := range handler.messages {
		if message == "forward auto-start rules failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("auto-start failure was not logged: %v", handler.messages)
	}
	if runtimes := fixture.forward.ListFor(fixture.ctx); len(runtimes) != 0 {
		t.Fatalf("failed auto-start left a runtime: %+v", runtimes)
	}
}

func TestForwardRuntimeRecoverStartupDefaultOffAndProductionSkip(t *testing.T) {
	fixture := newForwardRuntimeFixture(t, startEchoListener(t))
	var connects atomic.Int32
	outcomes := fixture.runtime.RecoverStartup(fixture.ctx, []forward.RecoveryDevice{{
		AssetID: fixture.asset.ID,
		Connect: func(context.Context) (string, error) { connects.Add(1); return "", nil },
	}})
	if len(outcomes) != 1 || outcomes[0].Skipped == "" || outcomes[0].Err != nil {
		t.Fatalf("default-off outcomes = %+v", outcomes)
	}
	if connects.Load() != 0 {
		t.Fatal("default-off recovery dialed a connection")
	}

	production, err := fixture.db.AssetCreate(fixture.ctx, store.AssetInput{
		Kind: "ssh", Name: "prod-host", OptionsJSON: "{}", Tags: "linux, production",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.saveAutoStartRule(t, production.ID, true)
	outcomes = fixture.runtime.RecoverStartup(fixture.ctx, []forward.RecoveryDevice{{
		AssetID: production.ID,
		Connect: func(context.Context) (string, error) { connects.Add(1); return "", nil },
	}})
	if len(outcomes) != 1 || outcomes[0].Skipped == "" || outcomes[0].Err != nil {
		t.Fatalf("production outcomes = %+v", outcomes)
	}
	if connects.Load() != 0 {
		t.Fatal("production asset was dialed during recovery")
	}
}

func TestForwardRuntimeRecoverStartupConnectsDeviceAndStartsRules(t *testing.T) {
	fixture := newForwardRuntimeFixture(t, startEchoListener(t))
	rule := fixture.saveAutoStartRule(t, fixture.asset.ID, true)
	device := fixture.runtime.ConnectDeviceForRecovery(fixture.ctx, fixture.asset.ID)

	outcomes := fixture.runtime.RecoverStartup(fixture.ctx, []forward.RecoveryDevice{device})
	if len(outcomes) != 1 || outcomes[0].Err != nil || outcomes[0].SessionID == "" {
		t.Fatalf("recover outcomes = %+v", outcomes)
	}
	if len(outcomes[0].Started) != 1 || outcomes[0].Started[0] != rule.ID {
		t.Fatalf("started rules = %v", outcomes[0].Started)
	}
	runtimes := fixture.forward.ListFor(fixture.ctx)
	if len(runtimes) != 1 || runtimes[0].ListenHost != "127.0.0.1" || runtimes[0].Status != forward.StatusActive {
		t.Fatalf("recovered runtimes = %+v", runtimes)
	}
	conn := assertForwardEcho(t, runtimes[0], "recovered")
	_ = conn.Close()
}

func TestForwardRuntimeStartShutdownRunsAndStopsReaper(t *testing.T) {
	fixture := newForwardRuntimeFixture(t, startEchoListener(t))
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.bin")
	destination := fslocal.New()
	upload, err := chunked.New("00000000000000000000000001", remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upload.WriteChunk(context.Background(), destination, runtimeChunk(0, "abcd")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, old, old)
	}); err != nil {
		t.Fatal(err)
	}
	fixture.runtime.reaper = chunked.NewScheduler(5*time.Millisecond, time.Hour, func(context.Context) ([]chunked.Target, error) {
		return []chunked.Target{{FileSystem: destination, Dir: dir}}, nil
	}, nil)

	if err := fixture.runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, ".remote.bin.nexterm-00000000000000000000000001.manifest")
	waitForCondition(t, 2*time.Second, func() bool {
		_, err := os.Stat(manifest)
		return errors.Is(err, os.ErrNotExist)
	})
	if err := fixture.runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	// After Shutdown the reaper no longer runs: a new staged group survives.
	second, err := chunked.New("00000000000000000000000002", remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.WriteChunk(context.Background(), destination, runtimeChunk(0, "wxyz")); err != nil {
		t.Fatal(err)
	}
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, old, old)
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, ".remote.bin.nexterm-00000000000000000000000002.manifest")); err != nil {
		t.Fatalf("reaper ran after Shutdown: %v", err)
	}
}

func TestForwardRuntimeResumeUploadStaging(t *testing.T) {
	fixture := newForwardRuntimeFixture(t, startEchoListener(t))
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.bin")
	destination := fslocal.New()
	first, err := chunked.New("00000000000000000000000001", remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteChunk(context.Background(), destination, runtimeChunk(0, "abcd")); err != nil {
		t.Fatal(err)
	}

	second, err := chunked.New("00000000000000000000000001", remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	status, err := fixture.runtime.ResumeUploadStaging(context.Background(), destination, second)
	if err != nil {
		t.Fatalf("ResumeUploadStaging: %v", err)
	}
	if status.Received != 4 || len(status.Chunks) != 1 || status.Chunks[0] != 0 {
		t.Fatalf("resume status = %+v", status)
	}
	if _, err := second.WriteChunk(context.Background(), destination, runtimeChunk(1, "efgh")); err != nil {
		t.Fatal(err)
	}
	committed, err := second.Commit(context.Background(), destination)
	if err != nil || committed != 8 {
		t.Fatalf("Commit = %d, %v", committed, err)
	}
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "abcdefgh" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
}

func TestForwardRuntimeNilForwardIsSafe(t *testing.T) {
	runtime := newForwardRuntime(forwardRuntimeConfig{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	runtime.SessionConnected(context.Background(), "s1")
	outcomes := runtime.RecoverStartup(context.Background(), []forward.RecoveryDevice{{AssetID: "asset-1"}})
	if len(outcomes) != 1 || outcomes[0].Err == nil {
		t.Fatalf("nil-forward outcomes = %+v", outcomes)
	}
	if removed, err := runtime.ReapStaleUploadStaging(context.Background()); err != nil || removed != 0 {
		t.Fatalf("nil-reaper sweep = %d, %v", removed, err)
	}
}
