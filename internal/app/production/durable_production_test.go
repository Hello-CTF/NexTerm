//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestProductionLocalAttachFallsBackToVolatileTabWithoutSupervisor(t *testing.T) {
	factory := &bridgeTestFactory{}
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir:            t.TempDir(),
		Desktop:            true,
		SupervisorStateDir: blockedSupervisorStateDir(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if production.Services.DurableErr == nil {
		t.Fatal("durable composition error is nil despite the blocked supervisor state directory")
	}
	if production.Services.Supervisor != nil {
		t.Fatal("supervisor is available despite the blocked state directory")
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connectedResponse := dispatchDurableTest(t, production, "session_connect_local", `null`, "", "")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)
	channelID := "missing-supervisor-channel"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	if tabID == "" {
		t.Fatal("volatile fallback attach returned an empty tab id")
	}
	stream := factory.at(channelID, 0)
	waitForProductionOutput(t, stream, "$ ")
	writeDurableTestCommand(t, production, tabID, "volatile-fallback-marker", "client-a")
	waitForProductionOutput(t, stream, "volatile-fallback-marker")
	tabs := production.Services.Sessions.ListTabs()
	if len(tabs) != 1 || tabs[0].ID != tabID {
		t.Fatalf("volatile fallback tabs = %+v", tabs)
	}
	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
	if tabs := production.Services.Sessions.ListTabs(); len(tabs) != 0 {
		t.Fatalf("volatile fallback tab survived close: %+v", tabs)
	}
}

func blockedSupervisorStateDir(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(blocker, "supervisor")
}

func dispatchDurableTest(t *testing.T, production *Production, command, args, channelID, clientID string) ipc.Response {
	t.Helper()
	return production.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: command, Args: json.RawMessage(args), Channel: ipc.ChannelRef{ID: channelID}, ClientID: clientID,
	}, production.Environment(clientID))
}

func writeDurableTestCommand(t *testing.T, production *Production, tabID, marker, clientID string) {
	t.Helper()
	barrier := filepath.Join(production.Services.dataDir, "durable-test-barrier-"+ids.New())
	writeDurableTestInput(t, production, tabID, "stty -echo", clientID)
	writeDurableTestInput(t, production, tabID, ": > "+barrier, clientID)
	waitForProductionFile(t, barrier)
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

func durableTestDataDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
