//go:build darwin || linux

package production

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

type deviceIPCTestRig struct {
	digest   string
	endpoint string
}

func newDeviceIPCTestRig(t *testing.T) *deviceIPCTestRig {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := supervisor.HelperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	server, err := supervisor.NewServer(instance, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := supervisor.StateDigest(stateDir, strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = instance.Close()
	})
	return &deviceIPCTestRig{digest: digest, endpoint: endpoint}
}

func (r *deviceIPCTestRig) connector(authorize *sync.Map) *DeviceConnectorConfig {
	return &DeviceConnectorConfig{
		Dial: func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", r.endpoint)
		},
		Digest: func(context.Context, string) (string, error) {
			return r.digest, nil
		},
		Authorize: func(_ context.Context, userID, deviceID string) error {
			authorize.Store(userID+":"+deviceID, true)
			return nil
		},
	}
}

func TestProductionDeviceSessionEndToEnd(t *testing.T) {
	rig := newDeviceIPCTestRig(t)
	factory := &bridgeTestFactory{}
	production := newTerminalTestProduction(t, durableTestDataDir(t), factory, nil)
	authorized := &sync.Map{}
	production.Services.SetDeviceConnector(rig.connector(authorized))

	connectResponse := dispatchDurableTest(t, production, "session_connect_device", `{"args":{"deviceId":"dev-1","name":"build-box"}}`, "", "client-a")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectResponse, &connected)
	if connected.Kind != "device" || connected.AssetID == nil || *connected.AssetID != "device/dev-1" {
		t.Fatalf("session = %+v, want device kind with pseudo asset id", connected)
	}
	if _, ok := authorized.Load(":dev-1"); !ok {
		t.Fatal("connect did not run the device authorize hook")
	}

	channelID := "device-e2e-channel"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	tabs := production.Services.Sessions.ListTabs()
	if len(tabs) != 1 || tabs[0].ID != tabID || !tabs[0].Durable {
		t.Fatalf("device tabs = %+v, want one durable tab", tabs)
	}
	stream := factory.at(channelID, 0)
	writeDurableTestCommand(t, production, tabID, "device-e2e-marker", "client-a")
	waitForProductionOutput(t, stream, "device-e2e-marker")

	// 断开后重连 (与前端 reconnectSessionAndWait 同路径): durable 标签经设备
	// daemon 重 attach, 重连后重新 attach_tab 接管, 输入输出恢复。
	requireProductionNull(t, dispatchDurableTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`, "", "client-a"))
	reconnectResponse := dispatchDurableTest(t, production, "session_reconnect", `{"sessionId":"`+connected.ID+`"}`, "", "client-a")
	var started bool
	requireStoreTestResponse(t, reconnectResponse, &started)
	if !started {
		t.Fatal("session_reconnect did not start")
	}
	waitForSessionStatus(t, production, connected.ID, "connected")

	recoverResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":65536}`, "device-e2e-recover-channel", "client-a")
	if !recoverResponse.OK {
		t.Fatalf("recover device tab = %+v, want ok", recoverResponse)
	}
	writeDurableTestCommand(t, production, tabID, "device-e2e-after-recover", "client-a")
	recoverStream := factory.at("device-e2e-recover-channel", 0)
	waitForProductionOutput(t, recoverStream, "device-e2e-after-recover")
}

func waitForSessionStatus(t *testing.T, production *Production, sessionID, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		for _, info := range production.Services.Sessions.ListSessions() {
			if info.ID == sessionID && string(info.Status) == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s never reached %s: %+v", sessionID, want, production.Services.Sessions.ListSessions())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProductionDeviceConnectValidationAndConfig(t *testing.T) {
	production := newTerminalTestProduction(t, durableTestDataDir(t), &bridgeTestFactory{}, nil)

	response := dispatchDurableTest(t, production, "session_connect_device", `{"args":{"deviceId":"","name":"x"}}`, "", "client-a")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("empty deviceId = %+v, want bad_param", response)
	}

	// 授权拒绝经 session_connect_device 原样透传 (owner/吊销/终端开关的拒绝文案)。
	production.Services.SetDeviceConnector(&DeviceConnectorConfig{
		Dial:   func(context.Context, string) (net.Conn, error) { return nil, errors.New("dial must not run") },
		Digest: func(context.Context, string) (string, error) { return "digest", nil },
		Authorize: func(context.Context, string, string) error {
			return ipc.NewError(ipc.CodeForbidden, "设备不属于该用户")
		},
	})
	response = dispatchDurableTest(t, production, "session_connect_device", `{"args":{"deviceId":"dev-1","name":"x"}}`, "", "client-a")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("authorize denied connect = %+v, want forbidden", response)
	}
	if !strings.Contains(response.Error.Message, "设备不属于该用户") {
		t.Fatalf("deny message = %q, want the authorize reason", response.Error.Message)
	}
}
