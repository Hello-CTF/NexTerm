//go:build darwin || linux

package production

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func TestProductionDeviceReconnectRejectsRecreatedIncarnation(t *testing.T) {
	rig := newDeviceIPCTestRig(t)
	config := rig.connector(&sync.Map{})
	injector := newDeviceBridgeFaultInjector(config.Dial)
	config.Dial = injector.Dial
	t.Cleanup(injector.close)
	production := newTerminalTestProduction(t, durableTestDataDir(t), &bridgeTestFactory{}, nil)
	production.Services.SetDeviceConnector(config)

	connectResponse := dispatchDurableTest(t, production, "session_connect_device", `{"args":{"deviceId":"dev-aba","name":"build-box"}}`, "", "client-a")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectResponse, &connected)
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "device-aba-channel", "client-a")
	tabID := requireAttachedTabID(t, attachResponse)
	before := deviceTestDurableInfo(t, rig, tabID)
	if before.Dead {
		t.Fatalf("durable process is dead before ABA injection: %+v", before)
	}

	injector.blockReconnects()
	injector.dropActive(t)
	select {
	case <-injector.dialStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("active attach failure did not trigger a reconnect dial")
	}

	client := supervisor.NewClient(rig.endpoint, rig.stateDir)
	oldIdentity := &supervisor.Identity{CreatedAt: before.CreatedAt, Incarnation: before.Incarnation}
	if err := client.Kill(t.Context(), tabID, oldIdentity); err != nil {
		t.Fatalf("kill original incarnation during reconnect: %v", err)
	}
	replacementInfo, err := client.Create(t.Context(), supervisor.CreateOptions{
		ID: tabID, Command: []string{"/bin/sh"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("create same-ID replacement during reconnect: %v", err)
	}
	if replacementInfo.Incarnation == before.Incarnation {
		t.Fatal("replacement reused the original incarnation")
	}
	replacement, err := client.Attach(t.Context(), tabID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()
	deviceTestWriteAndReadMarker(t, replacement, "printf 'device-replacement-%s\\n' 'marker'\r", "device-replacement-marker")

	injector.releaseReconnects()
	waitForSessionStatus(t, production, connected.ID, "connected")
	tab, err := production.Services.Sessions.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tab.Info(); !got.Exited || got.Durable {
		t.Fatalf("tab after same-ID replacement = %+v; want original incarnation gone without a durable attachment", got)
	}
	raw, err := production.Services.Sessions.RawDump(tabID, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "device-replacement-marker") {
		t.Fatalf("replacement process output was spliced into the original tab: %q", raw)
	}
	if err := production.Services.Sessions.Write(t.Context(), tabID, "client-a", []byte("printf 'must-not-run\\n'\r")); !errors.Is(err, session.ErrTabClosed) {
		t.Fatalf("write after original incarnation was rejected = %v; want ErrTabClosed", err)
	}

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","sessionId":"`+connected.ID+`","mode":"kill"}`, "", "client-a"))
	after := deviceTestDurableInfo(t, rig, tabID)
	if after.Dead || after.Incarnation != replacementInfo.Incarnation || !after.CreatedAt.Equal(replacementInfo.CreatedAt) {
		t.Fatalf("cleanup of the original tab affected the replacement incarnation: %+v", after)
	}
	deviceTestWriteAndReadMarker(t, replacement, "printf 'device-replacement-alive-%s\\n' 'marker'\r", "device-replacement-alive-marker")
}

func deviceTestWriteAndReadMarker(t *testing.T, stream *supervisor.Stream, command, marker string) string {
	t.Helper()
	if _, err := stream.Write([]byte(command)); err != nil {
		t.Fatalf("write helper marker %q: %v", marker, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	buffer := make([]byte, 4096)
	var output strings.Builder
	for !strings.Contains(output.String(), marker) {
		if time.Now().After(deadline) {
			t.Fatalf("helper did not return marker %q: %q", marker, output.String())
		}
		count, err := stream.Read(buffer)
		if count > 0 {
			output.Write(buffer[:count])
		}
		if err != nil {
			t.Fatalf("read helper marker %q: %v", marker, err)
		}
	}
	return output.String()
}
