//go:build darwin || linux

package production

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/supervisor"
)

func TestProductionSupervisorLocalAttachWithoutTmux(t *testing.T) {
	dataDir := durableTestDataDir(t)
	factory := &bridgeTestFactory{}
	production := newSupervisorTestProduction(t, dataDir, factory, nil)
	connected := connectLocalDurableTest(t, production)
	channelID := "supervisor-attach-channel"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)

	info := requireSupervisorInfo(t, production.Services.Supervisor, tabID)
	if info.Dead {
		t.Fatalf("supervisor session is dead right after attach: %+v", info)
	}
	if info.Cols != 80 || info.Rows != 24 {
		t.Fatalf("supervisor session grid = %dx%d, want 80x24", info.Cols, info.Rows)
	}
	if info.Incarnation == "" || info.CreatedAt.IsZero() {
		t.Fatalf("supervisor identity is incomplete: %+v", info)
	}

	stream := factory.at(channelID, 0)
	writeDurableTestCommand(t, production, tabID, "supervisor-attach-marker", "client-a")
	waitForProductionOutput(t, stream, "supervisor-attach-marker")

	tabs := production.Services.Sessions.ListTabs()
	if len(tabs) != 1 || tabs[0].ID != tabID || !tabs[0].Durable {
		t.Fatalf("tabs = %+v", tabs)
	}

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
	if _, found := supervisorInfoByID(production.Services.Supervisor, tabID); found {
		t.Fatal("close-tab did not destroy the supervisor session")
	}
	if tabs := production.Services.Sessions.ListTabs(); len(tabs) != 0 {
		t.Fatalf("tabs survived close: %+v", tabs)
	}
}

func TestProductionSupervisorBackgroundContinuationDiscoveryAndReattach(t *testing.T) {
	dataDir := durableTestDataDir(t)
	factory := &bridgeTestFactory{}
	production := newSupervisorTestProduction(t, dataDir, factory, nil)
	connected := connectLocalDurableTest(t, production)
	channelID := "supervisor-background-channel"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	stream := factory.at(channelID, 0)

	echoBarrier := filepath.Join(dataDir, "supervisor-echo-off-sync")
	writeDurableTestInput(t, production, tabID, "stty -echo", "client-a")
	writeDurableTestInput(t, production, tabID, ": > "+echoBarrier, "client-a")
	waitForProductionFile(t, echoBarrier)
	writeDurableTestInput(t, production, tabID, "marker=background-marker", "client-a")
	writeDurableTestInput(t, production, tabID, "(sleep 1; printf '%s\\n' \"$marker\") &", "client-a")
	writeDurableTestInput(t, production, tabID, "printf 'armed-marker\\n'", "client-a")
	waitForProductionOutput(t, stream, "armed-marker")

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_detach", `{"tabId":"`+tabID+`","channelId":"`+channelID+`"}`, "", "client-a"))
	waitRetention(t, stream.isClosed)

	listResponse := dispatchDurableTest(t, production, "terminal_list", `null`, "", "")
	var live []liveTabDTO
	requireStoreTestResponse(t, listResponse, &live)
	if len(live) != 1 || live[0].TabID != tabID || live[0].Subscribers != 0 || live[0].Exited {
		t.Fatalf("background discovery = %+v", live)
	}

	dump := waitForProductionDump(t, production, tabID, "background-marker")
	if count := strings.Count(dump, "background-marker"); count != 1 {
		t.Fatalf("background marker count in dump = %d, want 1", count)
	}
	if bytes.Contains(stream.output(), []byte("background-marker")) {
		t.Fatal("detached channel kept receiving output")
	}

	reattachChannel := "supervisor-background-reattach"
	reattachResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":262144}`, reattachChannel, "client-a")
	var reattached attachedTabDTO
	requireStoreTestResponse(t, reattachResponse, &reattached)
	if reattached.TabID != tabID || reattached.Exited {
		t.Fatalf("reattached tab = %+v", reattached)
	}
	reattachedStream := factory.at(reattachChannel, 0)
	waitForProductionOutput(t, reattachedStream, "background-marker")
	if count := bytes.Count(reattachedStream.output(), []byte("background-marker")); count != 1 {
		t.Fatalf("background marker replayed %d times: %q", count, reattachedStream.output())
	}

	writeDurableTestCommand(t, production, tabID, "post-reattach-marker", "client-a")
	waitForProductionOutput(t, reattachedStream, "post-reattach-marker")
	if count := bytes.Count(reattachedStream.output(), []byte("background-marker")); count != 1 {
		t.Fatalf("background marker duplicated after new input: %d", count)
	}
	if count := bytes.Count(reattachedStream.output(), []byte("post-reattach-marker")); count != 1 {
		t.Fatalf("post-reattach marker duplicated: %d", count)
	}

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
}

func TestProductionSupervisorRestartRecoveryReplaysFinalOutputAndReportsExit(t *testing.T) {
	dataDir := durableTestDataDir(t)
	channelID := "supervisor-recovery-channel"
	firstLog := &controlEventLog{}
	firstFactory := &bridgeTestFactory{}
	first := newSupervisorTestProduction(t, dataDir, firstFactory, firstLog)
	connected := connectLocalDurableTest(t, first)
	attachResponse := dispatchDurableTest(t, first, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	writeDurableTestCommand(t, first, tabID, "first-supervisor-marker", "client-a")
	waitForProductionOutput(t, firstFactory.at(channelID, 0), "first-supervisor-marker")
	requireProductionNull(t, dispatchDurableTest(t, first, "terminal_resize", `{"tabId":"`+tabID+`","cols":100,"rows":30}`, "", "client-a"))
	highWater := firstLog.maxVersion(tabID)
	before := requireSupervisorInfo(t, first.Services.Supervisor, tabID)
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	secondLog := &controlEventLog{}
	secondFactory := &bridgeTestFactory{}
	second := newSupervisorTestProduction(t, dataDir, secondFactory, secondLog)
	recovered := requireSupervisorInfo(t, second.Services.Supervisor, tabID)
	if !recovered.Dead {
		t.Fatalf("recovered supervisor session is not dead: %+v", recovered)
	}
	if recovered.Incarnation != before.Incarnation || !recovered.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("recovered identity = %+v, want %+v", recovered, before)
	}

	recoveredResponse := dispatchDurableTest(t, second, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":262144}`, channelID, "client-a")
	var recoveredDTO attachedTabDTO
	requireStoreTestResponse(t, recoveredResponse, &recoveredDTO)
	if recoveredDTO.TabID != tabID || recoveredDTO.Cols != 100 || recoveredDTO.Rows != 30 {
		t.Fatalf("recovered tab = %+v, want the final 100x30 grid", recoveredDTO)
	}
	replayed := secondFactory.at(channelID, 0)
	waitForProductionOutput(t, replayed, "first-supervisor-marker")
	if count := bytes.Count(replayed.output(), []byte("first-supervisor-marker")); count != 1 {
		t.Fatalf("marker replayed %d times: %q", count, replayed.output())
	}
	waitForProductionControlExit(t, secondLog, tabID)
	after := secondLog.forTab(tabID)
	if len(after) == 0 {
		t.Fatal("recovery emitted no control event")
	}
	if after[0].Version <= highWater {
		t.Fatalf("first recovered event version = %d, want > high-water %d", after[0].Version, highWater)
	}

	writeResponse := dispatchDurableTest(t, second, "terminal_write", `{"args":{"tabId":"`+tabID+`","data":[101,99,104,111,13],"clientId":"client-a"}}`, "", "client-a")
	if writeResponse.OK {
		t.Fatal("write to a recovered dead session succeeded")
	}

	requireProductionNull(t, dispatchDurableTest(t, second, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
	if _, found := supervisorInfoByID(second.Services.Supervisor, tabID); found {
		t.Fatal("close-tab kept the recovered supervisor session")
	}
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	third := newSupervisorTestProduction(t, dataDir, &bridgeTestFactory{}, nil)
	missingResponse := dispatchDurableTest(t, third, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":1024}`, channelID, "client-a")
	if missingResponse.OK || missingResponse.Error == nil || missingResponse.Error.Code != ipc.CodeNotFound {
		t.Fatalf("attach after kill = %+v, want not_found", missingResponse)
	}
	bogusResponse := dispatchDurableTest(t, third, "terminal_attach_tab", `{"tabId":"not-a-supervisor-id","replayBytes":1024}`, channelID, "client-a")
	if bogusResponse.OK || bogusResponse.Error == nil || bogusResponse.Error.Code != ipc.CodeBadParam {
		t.Fatalf("attach with an invalid identity = %+v, want bad_param", bogusResponse)
	}
}

func TestProductionSupervisorMultiClientAttachAndControl(t *testing.T) {
	dataDir := durableTestDataDir(t)
	factory := &bridgeTestFactory{}
	production := newSupervisorTestProduction(t, dataDir, factory, nil)
	connected := connectLocalDurableTest(t, production)
	channelA := "supervisor-multi-a"
	channelB := "supervisor-multi-b"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelA, "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	streamA := factory.at(channelA, 0)
	writeDurableTestCommand(t, production, tabID, "multi-ready", "client-a")
	waitForProductionOutput(t, streamA, "multi-ready")

	attachB := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":262144}`, channelB, "client-b")
	var infoB attachedTabDTO
	requireStoreTestResponse(t, attachB, &infoB)
	if infoB.Controller == nil || *infoB.Controller != "client-a" {
		t.Fatalf("controller after second attach = %+v, want client-a", infoB.Controller)
	}
	streamB := factory.at(channelB, 0)
	waitForProductionOutput(t, streamB, "multi-ready")

	writeB := dispatchDurableTest(t, production, "terminal_write", `{"args":{"tabId":"`+tabID+`","data":[101,99,104,111,13],"clientId":"client-b"}}`, "", "client-b")
	if writeB.OK || writeB.Error == nil || writeB.Error.Code != ipc.CodeNotController {
		t.Fatalf("viewer write = %+v, want not_controller", writeB)
	}

	writeDurableTestCommand(t, production, tabID, "multi-broadcast", "client-a")
	waitForProductionOutput(t, streamA, "multi-broadcast")
	waitForProductionOutput(t, streamB, "multi-broadcast")

	claimResponse := dispatchDurableTest(t, production, "terminal_claim", `{"tabId":"`+tabID+`"}`, "", "client-b")
	var previous *string
	requireStoreTestResponse(t, claimResponse, &previous)
	if previous == nil || *previous != "client-a" {
		t.Fatalf("claim previous = %+v, want client-a", previous)
	}
	writeA := dispatchDurableTest(t, production, "terminal_write", `{"args":{"tabId":"`+tabID+`","data":[101,99,104,111,13],"clientId":"client-a"}}`, "", "client-a")
	if writeA.OK || writeA.Error == nil || writeA.Error.Code != ipc.CodeNotController {
		t.Fatalf("write after claim loss = %+v, want not_controller", writeA)
	}
	writeDurableTestCommand(t, production, tabID, "multi-after-claim", "client-b")
	waitForProductionOutput(t, streamA, "multi-after-claim")
	waitForProductionOutput(t, streamB, "multi-after-claim")

	listResponse := dispatchDurableTest(t, production, "terminal_list", `null`, "", "")
	var live []liveTabDTO
	requireStoreTestResponse(t, listResponse, &live)
	if len(live) != 1 || live[0].Subscribers != 2 || live[0].Viewers != 2 {
		t.Fatalf("multi-client list = %+v", live)
	}

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_detach", `{"tabId":"`+tabID+`","channelId":"`+channelB+`"}`, "", "client-b"))
	listResponse = dispatchDurableTest(t, production, "terminal_list", `null`, "", "")
	requireStoreTestResponse(t, listResponse, &live)
	if len(live) != 1 || live[0].Subscribers != 1 {
		t.Fatalf("list after detach = %+v", live)
	}

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-b"}`, "", "client-b"))
	if _, found := supervisorInfoByID(production.Services.Supervisor, tabID); found {
		t.Fatal("close-tab did not destroy the supervisor session")
	}
}

func TestProductionSupervisorShutdownKillsSessionsWithoutSurvivalClaims(t *testing.T) {
	dataDir := durableTestDataDir(t)
	factory := &bridgeTestFactory{}
	production := newSupervisorTestProduction(t, dataDir, factory, nil)
	connected := connectLocalDurableTest(t, production)
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "supervisor-shutdown-channel", "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	if info := requireSupervisorInfo(t, production.Services.Supervisor, tabID); info.Dead {
		t.Fatalf("supervisor session is dead before shutdown: %+v", info)
	}

	if err := production.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	infos, err := production.Services.Supervisor.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != tabID || !infos[0].Dead {
		t.Fatalf("supervisor sessions after shutdown = %+v", infos)
	}
	if err := production.Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown = %v", err)
	}

	reopened := newSupervisorTestProduction(t, dataDir, &bridgeTestFactory{}, nil)
	recovered := requireSupervisorInfo(t, reopened.Services.Supervisor, tabID)
	if !recovered.Dead {
		t.Fatalf("session survived the server process: %+v", recovered)
	}
}

func TestProductionSupervisorKeepsNonLocalTransportsNonDurable(t *testing.T) {
	factory := &bridgeTestFactory{}
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir: t.TempDir(), Desktop: true,
		Connector: fakeSSHConnector{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if production.Services.Supervisor == nil {
		t.Fatal("supervisor is not composed")
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })

	host := "127.0.0.1"
	port := int32(22)
	username := "root"
	assetRow, err := production.Services.Store.AssetCreate(t.Context(), store.AssetInput{
		Kind: "ssh", Name: "supervisor-non-local", Host: &host, Port: &port, Username: &username,
	})
	if err != nil {
		t.Fatal(err)
	}
	connectedResponse := dispatchDurableTest(t, production, "session_connect", `{"args":{"assetId":"`+assetRow.ID+`"}}`, "supervisor-ssh-channel", "client-a")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "supervisor-ssh-channel", "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	if infos, err := production.Services.Supervisor.List(context.Background()); err != nil || len(infos) != 0 {
		t.Fatalf("supervisor sessions for an SSH tab = %+v, %v", infos, err)
	}
	tabs := production.Services.Sessions.ListTabs()
	if len(tabs) != 1 || tabs[0].Durable {
		t.Fatalf("ssh tab = %+v", tabs)
	}

	local := connectLocalDurableTest(t, production)
	localAttach := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+local.ID+`","cols":80,"rows":24}`, "supervisor-local-channel", "client-a")
	var localTabID string
	requireStoreTestResponse(t, localAttach, &localTabID)
	if info := requireSupervisorInfo(t, production.Services.Supervisor, localTabID); info.Dead {
		t.Fatalf("local durable session is dead: %+v", info)
	}
}

func newSupervisorTestProduction(t *testing.T, dataDir string, factory *bridgeTestFactory, events ipc.Emitter) *Production {
	t.Helper()
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
			Events:  events,
		},
		DataDir: dataDir, Desktop: true, DurableBinary: "nexterm-no-such-tmux-binary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if production.Services.Supervisor == nil {
		t.Fatal("embedded supervisor is not composed")
	}
	if production.Services.DurableErr != nil {
		t.Fatalf("durable composition error = %v", production.Services.DurableErr)
	}
	if production.Services.Durable != nil {
		t.Fatal("tmux backend composed despite the active supervisor")
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	return production
}

func connectLocalDurableTest(t *testing.T, production *Production) sessionInfoDTO {
	t.Helper()
	connectedResponse := dispatchDurableTest(t, production, "session_connect_local", `null`, "", "")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)
	return connected
}

func supervisorInfoByID(instance *supervisor.Supervisor, id string) (supervisor.Info, bool) {
	infos, err := instance.List(context.Background())
	if err != nil {
		return supervisor.Info{}, false
	}
	for _, info := range infos {
		if info.ID == id {
			return info, true
		}
	}
	return supervisor.Info{}, false
}

func requireSupervisorInfo(t *testing.T, instance *supervisor.Supervisor, id string) supervisor.Info {
	t.Helper()
	info, found := supervisorInfoByID(instance, id)
	if !found {
		t.Fatalf("supervisor session %s not found", id)
	}
	return info
}

func waitForProductionDump(t *testing.T, production *Production, tabID, text string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		response := dispatchDurableTest(t, production, "terminal_dump", `{"tabId":"`+tabID+`","maxBytes":262144}`, "", "client-a")
		var dump string
		requireStoreTestResponse(t, response, &dump)
		if strings.Contains(dump, text) {
			return dump
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal dump never contained %q: %q", text, dump)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForProductionFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("shell never created %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForProductionControlExit(t *testing.T, log *controlEventLog, tabID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		for _, event := range log.forTab(tabID) {
			if event.Exited {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no exited control event for %s: %+v", tabID, log.forTab(tabID))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
