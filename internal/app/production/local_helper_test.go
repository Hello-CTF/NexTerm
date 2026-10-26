//go:build darwin || linux

package production

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func newLocalHelperProduction(t *testing.T, enabled bool) (*Production, *bridgeTestFactory) {
	t.Helper()
	dataDir := t.TempDir()
	stateDir := filepath.Join(dataDir, "durable", "supervisor")
	t.Cleanup(func() { _ = exec.Command("pkill", "-9", "-f", supervisor.HelperCommand+" --state-dir "+stateDir).Run() })
	factory := &bridgeTestFactory{}
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir:               dataDir,
		Desktop:               true,
		LocalHelper:           enabled,
		LocalHelperExecutable: os.Args[0],
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	return production, factory
}

func waitLocalHelperState(t *testing.T, production *Production, want string) localHelperStatusDTO {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var status localHelperStatusDTO
		requireStoreTestResponse(t, dispatchDurableTest(t, production, "local_helper_status", `null`, "", "client-a"), &status)
		if status.State == want {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("local helper status = %+v, want %s", status, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProductionLocalTerminalsAreDurableAndCloseToTheBackground(t *testing.T) {
	production, _ := newLocalHelperProduction(t, true)
	waitLocalHelperState(t, production, "ready")
	var local sessionInfoDTO
	requireStoreTestResponse(t, dispatchDurableTest(t, production, "session_connect_local", `null`, "local-channel", "client-a"), &local)

	var attached attachedTabDTO
	requireStoreTestResponse(t, dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+local.ID+`","cols":80,"rows":24}`, "local-channel", "client-a"), &attached)
	tab, err := production.Services.Sessions.Tab(attached.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); !info.Durable || info.Ephemeral || !info.Retainable {
		t.Fatalf("local tab = %+v, want durable and retainable", info)
	}

	// Closing without a mode sends a durable local terminal to the background.
	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+attached.TabID+`","clientId":"client-a"}`, "", "client-a"))
	var listed []durableDescriptorDTO
	requireStoreTestResponse(t, dispatchDurableTest(t, production, "terminal_list", `{"sessionId":"`+local.ID+`","durable":true}`, "", "client-a"), &listed)
	if len(listed) != 1 || listed[0].ID != attached.TabID || listed[0].Dead {
		t.Fatalf("background local terminals = %+v", listed)
	}

	// Terminating it ends the shell.
	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+attached.TabID+`","sessionId":"`+local.ID+`","clientId":"client-a","mode":"kill"}`, "", "client-a"))
	requireStoreTestResponse(t, dispatchDurableTest(t, production, "terminal_list", `{"sessionId":"`+local.ID+`","durable":true}`, "", "client-a"), &listed)
	for _, descriptor := range listed {
		if !descriptor.Dead {
			t.Fatalf("terminated local terminal still runs: %+v", listed)
		}
	}

	// An explicit regular request keeps a terminal in this process.
	var regular attachedTabDTO
	requireStoreTestResponse(t, dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+local.ID+`","cols":80,"rows":24,"durable":false}`, "regular-channel", "client-a"), &regular)
	regularTab, err := production.Services.Sessions.Tab(regular.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if info := regularTab.Info(); info.Durable || !info.Ephemeral {
		t.Fatalf("regular local tab = %+v", info)
	}
}

func TestProductionWithoutALocalHelperKeepsLocalTerminalsRegular(t *testing.T) {
	production, _ := newLocalHelperProduction(t, false)
	status := waitLocalHelperState(t, production, "disabled")
	if status.Reason == "" {
		t.Fatalf("disabled status has no reason: %+v", status)
	}
	var local sessionInfoDTO
	requireStoreTestResponse(t, dispatchDurableTest(t, production, "session_connect_local", `null`, "local-channel", "client-a"), &local)
	var attached attachedTabDTO
	requireStoreTestResponse(t, dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+local.ID+`","cols":80,"rows":24}`, "local-channel", "client-a"), &attached)
	tab, err := production.Services.Sessions.Tab(attached.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Durable || !info.Ephemeral || attached.FallbackNotice != "" {
		t.Fatalf("local tab without a helper = %+v (notice %q)", info, attached.FallbackNotice)
	}
	response := dispatchDurableTest(t, production, "local_helper_restart", `{"consentToken":"3:1:0"}`, "", "client-a")
	if response.OK {
		t.Fatal("restart without a local helper succeeded")
	}
}
