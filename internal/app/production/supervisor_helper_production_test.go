//go:build darwin || linux

package production

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/supervisor"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == supervisor.HelperCommand {
		os.Exit(supervisor.RunHelperCLI(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestProductionDesktopHelperSurvivesAppRestart(t *testing.T) {
	dataDir := durableTestDataDir(t)
	stateDir := filepath.Join(dataDir, "durable", "supervisor")
	t.Cleanup(func() { killProductionHelperProcesses(t, stateDir) })
	firstFactory := &bridgeTestFactory{}
	first := newHelperTestProduction(t, dataDir, firstFactory, nil)
	if !first.Services.SupervisorHelper.Spawned() {
		t.Fatal("first app start did not spawn the helper")
	}
	connected := connectLocalDurableTest(t, first)
	channelID := "helper-restart-channel"
	attachResponse := dispatchDurableTest(t, first, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	stream := firstFactory.at(channelID, 0)
	writeDurableTestCommand(t, first, tabID, "helper-app-marker", "client-a")
	waitForProductionOutput(t, stream, "helper-app-marker")
	requireProductionNull(t, dispatchDurableTest(t, first, "terminal_resize", `{"tabId":"`+tabID+`","cols":100,"rows":30}`, "", "client-a"))

	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	endpoint, err := supervisor.HelperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	client := supervisor.NewClient(endpoint, stateDir)
	infos, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("helper did not survive the app exit: %v", err)
	}
	if len(infos) != 1 || infos[0].ID != tabID || infos[0].Dead {
		t.Fatalf("supervised sessions after app exit = %+v", infos)
	}

	secondFactory := &bridgeTestFactory{}
	second := newHelperTestProduction(t, dataDir, secondFactory, nil)
	if second.Services.SupervisorHelper.Spawned() {
		t.Fatal("app restart spawned a second helper instead of reattaching")
	}
	recoveredResponse := dispatchDurableTest(t, second, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":262144}`, channelID, "client-a")
	var recovered attachedTabDTO
	requireStoreTestResponse(t, recoveredResponse, &recovered)
	if recovered.TabID != tabID || recovered.Exited || recovered.Cols != 100 || recovered.Rows != 30 {
		t.Fatalf("recovered tab = %+v, want the live session with its final 100x30 grid", recovered)
	}
	replayed := secondFactory.at(channelID, 0)
	waitForProductionOutput(t, replayed, "helper-app-marker")
	if count := strings.Count(string(replayed.output()), "helper-app-marker"); count != 1 {
		t.Fatalf("helper marker replayed %d times: %q", count, replayed.output())
	}
	writeDurableTestCommand(t, second, tabID, "helper-restart-marker", "client-a")
	waitForProductionOutput(t, replayed, "helper-restart-marker")

	requireProductionNull(t, dispatchDurableTest(t, second, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
	infos, err = client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("close-tab kept the supervised session: %+v", infos)
	}
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProductionDesktopHelperRestartReapsExitedSession(t *testing.T) {
	dataDir := durableTestDataDir(t)
	stateDir := filepath.Join(dataDir, "durable", "supervisor")
	t.Cleanup(func() { killProductionHelperProcesses(t, stateDir) })
	first := newHelperTestProduction(t, dataDir, &bridgeTestFactory{}, nil)
	connected := connectLocalDurableTest(t, first)
	channelID := "helper-exited-channel"
	attachResponse := dispatchDurableTest(t, first, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	writeDurableTestCommand(t, first, tabID, "helper-exited-marker", "client-a")
	writeDurableTestInput(t, first, tabID, "exit", "client-a")

	endpoint, err := supervisor.HelperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	client := supervisor.NewClient(endpoint, stateDir)
	deadline := time.Now().Add(30 * time.Second)
	for {
		infos, err := client.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(infos) == 1 && infos[0].ID == tabID && infos[0].Dead {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("supervised session never exited: %+v", infos)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	secondLog := &controlEventLog{}
	secondFactory := &bridgeTestFactory{}
	second := newHelperTestProduction(t, dataDir, secondFactory, secondLog)
	recoveredResponse := dispatchDurableTest(t, second, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":262144}`, channelID, "client-a")
	var recovered attachedTabDTO
	requireStoreTestResponse(t, recoveredResponse, &recovered)
	if recovered.TabID != tabID {
		t.Fatalf("recovered tab = %+v, want the exited session", recovered)
	}
	replayed := secondFactory.at(channelID, 0)
	waitForProductionOutput(t, replayed, "helper-exited-marker")
	waitForProductionControlExit(t, secondLog, tabID)

	requireProductionNull(t, dispatchDurableTest(t, second, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
	infos, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("close-tab kept the exited session: %+v", infos)
	}
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProductionDesktopHelperUnavailableKeepsVolatileTabs(t *testing.T) {
	factory := &bridgeTestFactory{}
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir:                 t.TempDir(),
		Desktop:                 true,
		DesktopSupervisorHelper: true,
		SupervisorStateDir:      blockedSupervisorStateDir(t),
		DurableBinary:           "nexterm-no-such-tmux-binary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if production.Services.Supervisor != nil {
		t.Fatal("embedded supervisor composed in helper mode")
	}
	if production.Services.SupervisorHelper != nil {
		t.Fatal("helper composed despite an unusable state directory")
	}
	if production.Services.DurableErr == nil {
		t.Fatal("helper failure was not surfaced through the durable error")
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })

	connected := connectLocalDurableTest(t, production)
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "helper-degraded-channel", "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	tabs := production.Services.Sessions.ListTabs()
	if len(tabs) != 1 || tabs[0].ID != tabID || tabs[0].Durable {
		t.Fatalf("tabs without the helper = %+v, want one volatile tab", tabs)
	}
}

func newHelperTestProduction(t *testing.T, dataDir string, factory *bridgeTestFactory, events ipc.Emitter) *Production {
	t.Helper()
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
			Events:  events,
		},
		DataDir:                 dataDir,
		Desktop:                 true,
		DesktopSupervisorHelper: true,
		DurableBinary:           "nexterm-no-such-tmux-binary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if production.Services.Supervisor != nil {
		t.Fatal("embedded supervisor composed in helper mode")
	}
	if production.Services.SupervisorHelper == nil {
		t.Fatal("desktop supervisor helper is not composed")
	}
	if production.Services.DurableErr != nil {
		t.Fatalf("durable composition error = %v", production.Services.DurableErr)
	}
	if production.Services.Durable != nil {
		t.Fatal("tmux backend composed despite the active helper")
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	return production
}

func killProductionHelperProcesses(t *testing.T, stateDir string) {
	t.Helper()
	cmd := exec.Command("pkill", "-9", "-f", supervisor.HelperCommand+" --state-dir "+stateDir)
	_ = cmd.Run()
}
