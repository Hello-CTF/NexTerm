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

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
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
	tabID := requireAttachedTabID(t, attachResponse)
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

func TestProductionSSHStartupCommandEnvAndEncodingEndToEnd(t *testing.T) {
	server := newSSHConnectorServer(t, nil)
	server.envReqs = make(chan [2]string, 4)
	server.shellInput = make(chan string, 4)
	production := newTerminalTestProduction(t, durableTestDataDir(t), &bridgeTestFactory{}, nil)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "startup-e2e-password", "secret")
	assetID := createConnectorTestAsset(t, production, "startup-e2e", "127.0.0.1", server.port(), "password", credID,
		`{"autoAcceptUnknownHost":true,"encoding":"gbk","startupCommand":"echo hello","env":{"NEXTERM_TEST":"1"}}`)

	connectedResponse := dispatchDurableTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`, "", "client-a")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)

	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "startup-e2e-channel", "client-a")
	tabID := requireAttachedTabID(t, attachResponse)
	if tabID == "" {
		t.Fatal("terminal_attach returned no tab")
	}

	select {
	case env := <-server.envReqs:
		if env != [2]string{"NEXTERM_TEST", "1"} {
			t.Fatalf("env request = %v, want NEXTERM_TEST=1", env)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("asset env was not delivered over SSH")
	}
	select {
	case input := <-server.shellInput:
		if input != "echo hello\n" {
			t.Fatalf("shell input = %q, want the startup command", input)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup command was not written into the terminal")
	}

	attachTabResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":1024}`, "startup-e2e-reattach", "client-a")
	var attached attachedTabDTO
	requireStoreTestResponse(t, attachTabResponse, &attached)
	if attached.Encoding != "gbk" {
		t.Fatalf("attached tab encoding = %q, want gbk", attached.Encoding)
	}

	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
}

func dispatchDurableTest(t *testing.T, production *Production, command, args, channelID, clientID string) ipc.Response {
	t.Helper()
	return production.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: command, Args: json.RawMessage(args), Channel: ipc.ChannelRef{ID: channelID}, ClientID: clientID,
	}, production.Environment(clientID))
}

func requireAttachedTabID(t *testing.T, response ipc.Response) string {
	t.Helper()
	var attached attachedTabDTO
	requireStoreTestResponse(t, response, &attached)
	return attached.TabID
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
