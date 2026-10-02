//go:build darwin || linux

package production

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/durable"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestProductionDurableRealTmuxRestartRecoveryAndIdentityKill(t *testing.T) {
	requireRealTmux(t)
	dataDir := durableTestDataDir(t)
	factory := &bridgeTestFactory{}
	production := newDurableTestProduction(t, dataDir, factory)
	connectedResponse := dispatchDurableTest(t, production, "session_connect_local", `null`, "", "")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)
	channelID := "durable-restart-channel"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	before := requireDurableInfo(t, production.Services.Durable, tabID)
	if before.PID == 0 || before.SessionID == "" || before.PaneID == "" {
		t.Fatalf("durable identity is incomplete: %+v", before)
	}

	firstStream := factory.at(channelID, 0)
	waitForProductionOutput(t, firstStream, "$ ")
	writeDurableTestCommand(t, production, tabID, "first-durable-marker", "client-a")
	waitForProductionOutput(t, firstStream, "first-durable-marker")
	if err := production.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	afterShutdown := requireDurableInfo(t, production.Services.Durable, tabID)
	requireSameDurableIdentity(t, before, afterShutdown)
	if afterShutdown.Dead {
		t.Fatalf("manager shutdown killed durable identity: %+v", afterShutdown)
	}

	reopenedFactory := &bridgeTestFactory{}
	reopened := newDurableTestProduction(t, dataDir, reopenedFactory)
	connectedResponse = dispatchDurableTest(t, reopened, "session_connect_local", `null`, "", "")
	requireStoreTestResponse(t, connectedResponse, &connected)
	recoveredResponse := dispatchDurableTest(t, reopened, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":65536}`, channelID, "client-a")
	var recovered attachedTabDTO
	requireStoreTestResponse(t, recoveredResponse, &recovered)
	if recovered.TabID != tabID || recovered.Cols != 80 || recovered.Rows != 24 {
		t.Fatalf("recovered tab metadata = %+v", recovered)
	}
	afterRecovery := requireDurableInfo(t, reopened.Services.Durable, tabID)
	requireSameDurableIdentity(t, before, afterRecovery)
	replayed := reopenedFactory.at(channelID, 0)
	waitForProductionOutput(t, replayed, "first-durable-marker")
	if count := bytes.Count(replayed.output(), []byte("first-durable-marker")); count != 1 {
		t.Fatalf("first marker was rerun or duplicated %d times: %q", count, replayed.output())
	}
	writeDurableTestCommand(t, reopened, tabID, "second-durable-marker", "client-a")
	waitForProductionOutput(t, replayed, "second-durable-marker")
	if count := bytes.Count(replayed.output(), []byte("first-durable-marker")); count != 1 {
		t.Fatalf("old output was duplicated after new input %d times", count)
	}
	if count := bytes.Count(replayed.output(), []byte("second-durable-marker")); count != 1 {
		t.Fatalf("new output was duplicated %d times", count)
	}

	requireProductionNull(t, dispatchDurableTest(t, reopened, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
	if _, found := durableInfoByID(reopened.Services.Durable, tabID); found {
		t.Fatal("explicit close-tab did not destroy the durable identity")
	}
	if err := reopened.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	final := newDurableTestProduction(t, dataDir, &bridgeTestFactory{})
	missingResponse := dispatchDurableTest(t, final, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":65536}`, channelID, "client-a")
	if missingResponse.OK || missingResponse.Error == nil || missingResponse.Error.Code != ipc.CodeNotFound {
		t.Fatalf("recover after identity kill = %+v, want not_found", missingResponse)
	}
	if _, found := durableInfoByID(final.Services.Durable, tabID); found {
		t.Fatal("killed identity was recreated by a volatile fallback")
	}
	if err := final.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProductionDurableMissingTmuxIsUnavailableWithoutFallback(t *testing.T) {
	factory := &bridgeTestFactory{}
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir: t.TempDir(), Desktop: true, DurableBinary: "nexterm-no-such-tmux-binary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(production.Services.DurableErr, durable.ErrUnavailable) {
		t.Fatalf("durable constructor error = %v", production.Services.DurableErr)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connectedResponse := dispatchDurableTest(t, production, "session_connect_local", `null`, "", "")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)
	response := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "missing-tmux-channel", "client-a")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("missing tmux response = %+v, want unsupported", response)
	}
	if len(factory.streams) != 0 {
		t.Fatal("volatile fallback opened a terminal stream")
	}
	if tabs := production.Services.Sessions.ListTabs(); len(tabs) != 0 {
		t.Fatalf("volatile fallback created tabs: %+v", tabs)
	}
}

func newDurableTestProduction(t *testing.T, dataDir string, factory *bridgeTestFactory) *Production {
	t.Helper()
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir: dataDir, Desktop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	return production
}

func dispatchDurableTest(t *testing.T, production *Production, command, args, channelID, clientID string) ipc.Response {
	t.Helper()
	return production.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: command, Args: json.RawMessage(args), Channel: ipc.ChannelRef{ID: channelID}, ClientID: clientID,
	}, production.Environment(clientID))
}

func writeDurableTestCommand(t *testing.T, production *Production, tabID, marker, clientID string) {
	t.Helper()
	writeDurableTestInput(t, production, tabID, "stty -echo", clientID)
	writeDurableTestInput(t, production, tabID, "printf '"+marker+"\\n'", clientID)
	writeDurableTestInput(t, production, tabID, "stty echo", clientID)
}

func writeDurableTestInput(t *testing.T, production *Production, tabID, input, clientID string) {
	t.Helper()
	command := []byte(input + "\r")
	values := make([]int, len(command))
	for index, value := range command {
		values[index] = int(value)
	}
	if _, err := productionBytes(values); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"args": map[string]any{"tabId": tabID, "data": values, "clientId": clientID}})
	if err != nil {
		t.Fatal(err)
	}
	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_write", string(payload), "", clientID))
}

func requireDurableInfo(t *testing.T, backend *durable.Backend, id string) durable.Info {
	t.Helper()
	info, found := durableInfoByID(backend, id)
	if !found {
		t.Fatalf("durable identity %s not found", id)
	}
	return info
}

func durableInfoByID(backend *durable.Backend, id string) (durable.Info, bool) {
	infos, err := backend.List(context.Background())
	if err != nil {
		return durable.Info{}, false
	}
	for _, info := range infos {
		if info.ID == id {
			return info, true
		}
	}
	return durable.Info{}, false
}

func requireSameDurableIdentity(t *testing.T, left, right durable.Info) {
	t.Helper()
	if left.ID != right.ID || left.SessionID != right.SessionID || left.PaneID != right.PaneID || left.PID != right.PID {
		t.Fatalf("identity changed: before=%+v after=%+v", left, right)
	}
}

func durableTestDataDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func requireRealTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("real tmux is unavailable")
	}
	root := durableTestDataDir(t)
	if _, err := durable.New(durable.Config{SocketPath: root + "/tmux.sock", StateDir: root}); err != nil {
		t.Skipf("real tmux backend is unavailable: %v", err)
	}
}
