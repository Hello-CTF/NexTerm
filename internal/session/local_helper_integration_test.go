//go:build darwin || linux

package session

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/localhelper"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

// startLocalHelper runs the real local helper lifecycle with this test binary
// as the helper executable (TestMain serves the helper command).
func startLocalHelper(t *testing.T, stateDir string) *localhelper.Service {
	t.Helper()
	service := localhelper.New(localhelper.Config{
		StateDir: stateDir, Executable: os.Args[0], Logger: slog.New(slog.DiscardHandler),
	})
	service.Start()
	t.Cleanup(service.Close)
	if _, err := service.WaitReady(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("local helper did not become ready: %v (%+v)", err, service.Status())
	}
	return service
}

func killLocalHelper(t *testing.T, stateDir string) {
	t.Helper()
	t.Cleanup(func() { _ = exec.Command("pkill", "-9", "-f", supervisor.HelperCommand+" --state-dir "+stateDir).Run() })
}

func localHelperManager(t *testing.T, source LocalDurableSource) (*Manager, *Session) {
	t.Helper()
	connector := ConnectorFunc(func(_ context.Context, _ Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := NewManager(Config{Connector: connector, LocalDurable: source, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "local-helper", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	return manager, connected
}

func openLocalTab(t *testing.T, manager *Manager, connected *Session, client string) TabInfo {
	t.Helper()
	info, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, ClientID: client, ChannelID: client + "-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func writeAndWait(t *testing.T, manager *Manager, tabID, client, marker string) {
	t.Helper()
	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(context.Background(), tabID, client, []byte("printf 'nexterm-%s\\n' '"+marker+"'\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(tab.terminal.Dump(1<<20), []byte("nexterm-"+marker)) })
}

func listedIDs(t *testing.T, manager *Manager, sessionID string) []string {
	t.Helper()
	descriptors, err := manager.ListRemoteDurable(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if !descriptor.Dead {
			ids = append(ids, descriptor.ID)
		}
	}
	return ids
}

func TestLocalTerminalSurvivesAppExitAndReattachesWithContinuousOutput(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	killLocalHelper(t, stateDir)
	service := startLocalHelper(t, stateDir)
	manager, connected := localHelperManager(t, service)
	info := openLocalTab(t, manager, connected, "client-a")
	if !info.Durable || info.Ephemeral || info.FallbackNotice != "" {
		t.Fatalf("local tab = %+v, want a durable tab without a fallback notice", info)
	}
	writeAndWait(t, manager, info.ID, "client-a", "before-exit")

	// Closing the tab moves it to the background: the shell keeps running.
	if err := manager.BackgroundTab(context.Background(), info.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if ids := listedIDs(t, manager, connected.ID); !slices.Contains(ids, info.ID) {
		t.Fatalf("background local terminal missing from the helper list: %v", ids)
	}

	// The app exits: the manager and the helper lifecycle close, the helper
	// and its shell keep running.
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	service.Close()
	running, err := supervisor.InspectHelper(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("helper did not survive the app: %v", err)
	}
	if running.Activity.PTY != 1 {
		t.Fatalf("helper activity after app exit = %+v, want the shell still running", running.Activity)
	}

	// The app starts again and takes the terminal over.
	reopened := startLocalHelper(t, stateDir)
	if status := reopened.Status(); status.State != localhelper.StateReady {
		t.Fatalf("restarted app status = %+v", status)
	}
	again, reconnected := localHelperManager(t, reopened)
	if ids := listedIDs(t, again, reconnected.ID); !slices.Contains(ids, info.ID) {
		t.Fatalf("restarted app does not list the local terminal: %v", ids)
	}
	recovered, err := again.RecoverRemoteDurable(context.Background(), info.ID, OpenTabOptions{
		SessionID: reconnected.ID, ClientID: "client-b", ChannelID: "client-b-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != info.ID || !recovered.Durable {
		t.Fatalf("recovered tab = %+v", recovered)
	}
	tab, err := again.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(tab.terminal.Dump(1<<20), []byte("nexterm-before-exit")) })
	writeAndWait(t, again, info.ID, "client-b", "after-reopen")
	dump := tab.terminal.Dump(1 << 20)
	if before, after := bytes.Index(dump, []byte("nexterm-before-exit")), bytes.LastIndex(dump, []byte("nexterm-after-reopen")); before < 0 || after < before {
		t.Fatalf("output is not continuous across the app restart: %q", dump)
	}

	// Terminating the session ends the shell in the helper.
	if err := again.CloseTab(info.ID); err != nil {
		t.Fatal(err)
	}
	if ids := listedIDs(t, again, reconnected.ID); slices.Contains(ids, info.ID) {
		t.Fatalf("terminated local terminal is still listed: %v", ids)
	}
	waitFor(t, func() bool {
		running, err := supervisor.InspectHelper(context.Background(), stateDir)
		return err == nil && running.Activity.PTY == 0
	})
}

func TestLocalTerminalTerminateFromTheBackgroundListKills(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	killLocalHelper(t, stateDir)
	service := startLocalHelper(t, stateDir)
	manager, connected := localHelperManager(t, service)
	info := openLocalTab(t, manager, connected, "client-a")
	writeAndWait(t, manager, info.ID, "client-a", "kill-me")
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	// A new app instance kills a terminal it never attached, the way the
	// background list's "terminate session" does.
	again, reconnected := localHelperManager(t, service)
	if _, err := again.RecoverRemoteDurable(context.Background(), info.ID, OpenTabOptions{
		SessionID: reconnected.ID, ClientID: "client-c", Detached: true, Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}
	if err := again.CloseTab(info.ID); err != nil {
		t.Fatal(err)
	}
	if ids := listedIDs(t, again, reconnected.ID); len(ids) != 0 {
		t.Fatalf("helper still lists %v after terminate", ids)
	}
}

type unavailableLocalSource struct{ status localhelper.Status }

func (s unavailableLocalSource) WaitReady(context.Context, time.Duration) (base.DurableProvider, error) {
	return nil, &localhelper.UnavailableError{Status: s.status}
}

func (s unavailableLocalSource) WaitExisting(ctx context.Context, limit time.Duration) (base.DurableProvider, error) {
	return s.WaitReady(ctx, limit)
}

func TestLocalTerminalFallsBackToRegularWithAVisibleReason(t *testing.T) {
	source := unavailableLocalSource{status: localhelper.Status{State: localhelper.StateFailed, Reason: "本机持久终端组件连续 3 次启动后立即退出"}}
	manager, connected := localHelperManager(t, source)
	first := openLocalTab(t, manager, connected, "client-a")
	if first.Durable || !first.Ephemeral || !first.Retainable {
		t.Fatalf("fallback tab = %+v, want a regular retainable local tab", first)
	}
	if first.FallbackNotice == "" || !bytes.Contains([]byte(first.FallbackNotice), []byte("连续 3 次")) {
		t.Fatalf("fallback notice = %q, want the helper reason", first.FallbackNotice)
	}
	writeAndWait(t, manager, first.ID, "client-a", "regular-ok")
	second := openLocalTab(t, manager, connected, "client-b")
	if second.FallbackNotice != "" {
		t.Fatalf("the same reason was repeated on the next tab: %q", second.FallbackNotice)
	}
	// Explicit paths on existing terminals report the reason instead of
	// silently returning nothing.
	_, err := manager.ListRemoteDurable(context.Background(), connected.ID)
	var unavailable *localhelper.UnavailableError
	if !errors.As(err, &unavailable) || !errors.Is(err, durable.ErrUnavailable) {
		t.Fatalf("list with an unavailable helper = %v", err)
	}
	readiness, err := manager.PrepareDurable(WithOpenAccess(context.Background()), connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != DurableUnavailable || readiness.Reason == "" {
		t.Fatalf("readiness = %+v", readiness)
	}
}

func TestLocalTerminalRegularAssetStaysRegularWithoutNotice(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	killLocalHelper(t, stateDir)
	service := startLocalHelper(t, stateDir)
	connector := ConnectorFunc(func(_ context.Context, _ Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := NewManager(Config{Connector: connector, LocalDurable: service, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "local-regular", Kind: KindLocal, TerminalMode: TerminalModeRegular})
	if err != nil {
		t.Fatal(err)
	}
	readiness, err := manager.PrepareDurable(WithOpenAccess(context.Background()), connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != DurableDisabled {
		t.Fatalf("regular local asset readiness = %+v, want disabled", readiness)
	}
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if info.Durable || info.FallbackNotice != "" {
		t.Fatalf("regular local asset tab = %+v", info)
	}
}

func TestPrepareDurableReportsAReadyLocalHelper(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	killLocalHelper(t, stateDir)
	service := startLocalHelper(t, stateDir)
	manager, connected := localHelperManager(t, service)
	readiness, err := manager.PrepareDurable(WithOpenAccess(context.Background()), connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != DurableReady {
		t.Fatalf("readiness = %+v, want ready", readiness)
	}
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !info.Durable {
		t.Fatalf("agent-created local tab = %+v, want durable", info)
	}
}

// BenchmarkLocalFirstOutput measures the time from opening a local terminal
// to its first output (the shell prompt) through the helper and directly.
func BenchmarkLocalFirstOutput(b *testing.B) {
	stateDir := filepath.Join(b.TempDir(), "state")
	b.Cleanup(func() { _ = exec.Command("pkill", "-9", "-f", supervisor.HelperCommand+" --state-dir "+stateDir).Run() })
	service := localhelper.New(localhelper.Config{StateDir: stateDir, Executable: os.Args[0], Logger: slog.New(slog.DiscardHandler)})
	cold := time.Now()
	service.Start()
	b.Cleanup(service.Close)
	if _, err := service.WaitReady(context.Background(), 30*time.Second); err != nil {
		b.Fatal(err)
	}
	b.Logf("local helper cold start (spawn to ready): %s", time.Since(cold))
	for _, mode := range []string{"helper", "direct"} {
		b.Run(mode, func(b *testing.B) {
			connector := ConnectorFunc(func(_ context.Context, _ Asset, _ uint64) (base.Transport, error) {
				return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
			})
			manager := NewManager(Config{Connector: connector, LocalDurable: service})
			defer func() { _ = manager.Close() }()
			connected, err := manager.Connect(context.Background(), Asset{ID: "bench-" + mode, Kind: KindLocal})
			if err != nil {
				b.Fatal(err)
			}
			for index := 0; index < b.N; index++ {
				options := OpenTabOptions{SessionID: connected.ID, ClientID: "bench", ChannelID: "bench-1", Cols: 80, Rows: 24}
				if mode == "helper" {
					options.Durable = &DurableTabOptions{}
				} else {
					options.Ephemeral, options.Retainable = true, true
				}
				info, err := manager.OpenTab(context.Background(), options)
				if err != nil {
					b.Fatal(err)
				}
				tab, err := manager.Tab(info.ID)
				if err != nil {
					b.Fatal(err)
				}
				deadline := time.Now().Add(10 * time.Second)
				for len(bytes.TrimSpace(tab.terminal.Dump(1<<16))) == 0 {
					if time.Now().After(deadline) {
						b.Fatal("no first output")
					}
					time.Sleep(time.Millisecond)
				}
				b.StopTimer()
				if err := manager.CloseTab(info.ID); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
			}
		})
	}
}

func TestLocalTerminalListingWithoutARunningHelperSpawnsNothing(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	killLocalHelper(t, stateDir)
	service := localhelper.New(localhelper.Config{StateDir: stateDir, Executable: os.Args[0], Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(service.Close)
	manager, connected := localHelperManager(t, service)
	if ids := listedIDs(t, manager, connected.ID); len(ids) != 0 {
		t.Fatalf("terminals without a helper = %v", ids)
	}
	if _, err := manager.RecoverRemoteDurable(context.Background(), "0123456789abcdef0123456789abcdef", OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", Detached: true, Cols: 80, Rows: 24,
	}); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("recover without a helper = %v, want ErrTabNotFound", err)
	}
	if state := service.Status().State; state != localhelper.StateIdle {
		t.Fatalf("listing started a helper: %s", state)
	}
	if _, err := supervisor.InspectHelper(context.Background(), stateDir); err == nil {
		t.Fatal("a helper runs although only existing terminals were listed")
	}
}

func TestLocalTerminalModeRegularKeepsAgentTerminalsInTheAppWithoutAHelper(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	killLocalHelper(t, stateDir)
	service := localhelper.New(localhelper.Config{StateDir: stateDir, Executable: os.Args[0], Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(service.Close)
	connector := ConnectorFunc(func(_ context.Context, _ Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := NewManager(Config{
		Connector: connector, LocalDurable: service, OpenAccess: true,
		LocalTerminalMode: func(context.Context) (string, error) { return TerminalModeRegular, nil },
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "local-device-regular", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if info.Durable || info.FallbackNotice != "" {
		t.Fatalf("agent tab with regular local terminals = %+v", info)
	}
	readiness, err := manager.PrepareDurable(WithOpenAccess(context.Background()), connected.ID)
	if err != nil || readiness.State != DurableDisabled {
		t.Fatalf("readiness = %+v, %v; want disabled", readiness, err)
	}
	if _, err := supervisor.InspectHelper(context.Background(), stateDir); err == nil {
		t.Fatal("a helper was spawned although local terminals are regular")
	}
}
