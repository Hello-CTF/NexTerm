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
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestProductionLocalAttachIsVolatileAndEndsOnDetach(t *testing.T) {
	factory := &bridgeTestFactory{}
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir: t.TempDir(),
		Desktop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connected := connectLocalTerminalTest(t, production)
	channelID := "local-volatile-channel"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	if tabID == "" {
		t.Fatal("volatile attach returned an empty tab id")
	}
	stream := factory.at(channelID, 0)
	waitForProductionOutput(t, stream, "$ ")
	writeDurableTestCommand(t, production, tabID, "volatile-local-marker", "client-a")
	waitForProductionOutput(t, stream, "volatile-local-marker")
	tabs := production.Services.Sessions.ListTabs()
	if len(tabs) != 1 || tabs[0].ID != tabID || tabs[0].Durable {
		t.Fatalf("local tabs = %+v, want one volatile tab", tabs)
	}

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_detach", `{"tabId":"`+tabID+`","channelId":"`+channelID+`"}`, "", "client-a"))
	if tabs := production.Services.Sessions.ListTabs(); len(tabs) != 0 {
		t.Fatalf("local tab survived detach: %+v", tabs)
	}
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
