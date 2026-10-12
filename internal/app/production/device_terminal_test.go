//go:build unix

package production

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bytes"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

type deviceTerminalEventRecorder struct {
	mu     sync.Mutex
	events []ipc.Event
}

func (r *deviceTerminalEventRecorder) Emit(_ context.Context, event ipc.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *deviceTerminalEventRecorder) waitFor(t *testing.T, match func(ipc.Event) bool, what string) ipc.Event {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, event := range r.events {
			if match(event) {
				r.mu.Unlock()
				return event
			}
		}
		r.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return ipc.Event{}
}

type deviceTerminalTestRig struct {
	manager    *deviceTerminalManager
	events     *deviceTerminalEventRecorder
	dispatcher *ipc.Dispatcher
	instance   *supervisor.Supervisor
	server     *supervisor.Server
	digest     string
}

// newDeviceTerminalTestRig 起真 supervisor + socket server (supervisor 包公开基件,
// 与 internal/fleet/agent 测试同一形态), 桥管理器的 WS 拨号替换为 unix socket 直连,
// 帧编解码走真实 supervisor v2 协议栈。
func newDeviceTerminalTestRig(t *testing.T) *deviceTerminalTestRig {
	t.Helper()
	return newDeviceTerminalTestRigWithDial(t, nil)
}

func newDeviceTerminalTestRigWithDial(t *testing.T, wrap func(deviceTerminalDial) deviceTerminalDial) *deviceTerminalTestRig {
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
	dial := deviceTerminalDial(func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", endpoint)
	})
	if wrap != nil {
		dial = wrap(dial)
	}
	events := &deviceTerminalEventRecorder{}
	manager := newDeviceTerminalManager(dial, events)
	dispatcher := ipc.NewDispatcher()
	if err := registerDeviceTerminalCommands(dispatcher, manager); err != nil {
		t.Fatal(err)
	}
	rig := &deviceTerminalTestRig{manager: manager, events: events, dispatcher: dispatcher, instance: instance, server: server, digest: digest}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = manager.Shutdown(shutdownCtx)
		_ = server.Shutdown(shutdownCtx)
		_ = instance.Close()
	})
	return rig
}

func dispatchDeviceTerminal(t *testing.T, rig *deviceTerminalTestRig, command, args string) ipc.Response {
	t.Helper()
	return rig.dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func mustOpenDeviceTerminal(t *testing.T, rig *deviceTerminalTestRig, digest string, cols, rows uint32) deviceTerminalOpenResponse {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"args": map[string]any{
		"deviceId": "dev-1", "stateDigest": digest, "cols": cols, "rows": rows,
	}})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalOpen, string(payload))
	if !response.OK {
		t.Fatalf("open failed: %+v", response.Error)
	}
	var opened deviceTerminalOpenResponse
	if err := json.Unmarshal(response.Data, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.ChannelID == "" || opened.Info.ID == "" || opened.Identity.Incarnation == "" || opened.Identity.CreatedAtUnixNano == "" {
		t.Fatalf("open response incomplete: %+v", opened)
	}
	if begin := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalBegin, `{"args":{"channelId":"`+opened.ChannelID+`"}}`); !begin.OK {
		t.Fatalf("begin failed: %+v", begin.Error)
	}
	return opened
}

func (rig *deviceTerminalTestRig) listSessions(t *testing.T) []supervisor.Info {
	t.Helper()
	client := supervisor.NewRemoteClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", rig.server.SocketPath())
	}, rig.digest)
	infos, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return infos
}

func waitOutputContains(t *testing.T, rig *deviceTerminalTestRig, channelID, marker string) {
	t.Helper()
	rig.events.waitFor(t, func(event ipc.Event) bool {
		if event.Event != TopicDeviceTerminalOutput {
			return false
		}
		output, ok := event.Payload.(DeviceTerminalOutputEvent)
		return ok && output.ChannelID == channelID && bytes.Contains(output.Data, []byte(marker))
	}, "output containing "+marker)
}

func sendDeviceTerminalInput(t *testing.T, rig *deviceTerminalTestRig, channelID string, data []byte) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"args": map[string]any{
		"channelId": channelID, "data": data,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalInput, string(payload)); !response.OK {
		t.Fatalf("input failed: %+v", response.Error)
	}
}

// waitShellReady 等 shell 就绪: 通道上出现首个 output (prompt) 即 readline 已
// 开始读 PTY; 更早到达的输入会被 shell 初始化丢弃 (PTY 测试的经典竞态)。
func waitShellReady(t *testing.T, rig *deviceTerminalTestRig, channelID string) {
	t.Helper()
	rig.events.waitFor(t, func(event ipc.Event) bool {
		if event.Event != TopicDeviceTerminalOutput {
			return false
		}
		output, ok := event.Payload.(DeviceTerminalOutputEvent)
		return ok && output.ChannelID == channelID
	}, "first shell output")
}

// driveEchoMarker 循环发送 echo 直到 marker 出现在输出里; echo 幂等, 重发无害。
func driveEchoMarker(t *testing.T, rig *deviceTerminalTestRig, channelID, marker string) {
	t.Helper()
	waitShellReady(t, rig, channelID)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		sendDeviceTerminalInput(t, rig, channelID, []byte("echo "+marker+"\n"))
		if outputSeenWithin(rig, channelID, marker, 700*time.Millisecond) {
			return
		}
	}
	t.Fatalf("marker %q never appeared in output", marker)
}

func outputSeenWithin(rig *deviceTerminalTestRig, channelID, marker string, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		rig.events.mu.Lock()
		found := false
		for _, event := range rig.events.events {
			if event.Event != TopicDeviceTerminalOutput {
				continue
			}
			if output, ok := event.Payload.(DeviceTerminalOutputEvent); ok && output.ChannelID == channelID && bytes.Contains(output.Data, []byte(marker)) {
				found = true
				break
			}
		}
		rig.events.mu.Unlock()
		if found {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestDeviceTerminalCommandsRegistered(t *testing.T) {
	production := newUpdateTestProduction(t, false, nil)
	registered := map[string]bool{}
	for _, command := range production.Dispatcher.Commands() {
		registered[command] = true
	}
	for _, command := range []string{
		CommandDeviceTerminalOpen, CommandDeviceTerminalResume, CommandDeviceTerminalBegin,
		CommandDeviceTerminalInput, CommandDeviceTerminalResize, CommandDeviceTerminalKill,
		CommandDeviceTerminalKillSession, CommandDeviceTerminalClose,
	} {
		if !registered[command] {
			t.Fatalf("production is missing %s", command)
		}
	}
}

func TestDeviceTerminalLifecycle(t *testing.T) {
	rig := newDeviceTerminalTestRig(t)
	opened := mustOpenDeviceTerminal(t, rig, rig.digest, 80, 24)

	marker := "dt-marker-" + opened.Info.ID
	driveEchoMarker(t, rig, opened.ChannelID, marker)

	resizeResponse := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalResize, `{"args":{"channelId":"`+opened.ChannelID+`","cols":100,"rows":40}}`)
	if !resizeResponse.OK {
		t.Fatalf("resize failed: %+v", resizeResponse.Error)
	}

	// close 是 detach 语义: 设备端会话保留, 通道移除。
	closeResponse := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalClose, `{"args":{"channelId":"`+opened.ChannelID+`"}}`)
	if !closeResponse.OK {
		t.Fatalf("close failed: %+v", closeResponse.Error)
	}
	if again := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalInput, `{"args":{"channelId":"`+opened.ChannelID+`","data":[65]}}`); again.OK {
		t.Fatal("input on a closed channel must fail")
	}
	if sessions := rig.listSessions(t); len(sessions) != 1 {
		t.Fatalf("detached session must survive close, got %+v", sessions)
	}

	// 身份不匹配的重 attach 必须失败且会话保留。
	badResume, err := json.Marshal(map[string]any{"args": map[string]any{
		"deviceId": "dev-1", "stateDigest": rig.digest, "sessionId": opened.Info.ID,
		"identity": map[string]any{"createdAtUnixNano": opened.Identity.CreatedAtUnixNano, "incarnation": "bogus"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalResume, string(badResume)); response.OK {
		t.Fatal("resume with a wrong incarnation must fail")
	}
	if sessions := rig.listSessions(t); len(sessions) != 1 {
		t.Fatalf("failed resume must keep the session, got %+v", sessions)
	}

	// resume 成功后从 seq 0 重放 backlog: 断开期间的输出不丢。
	goodResume, err := json.Marshal(map[string]any{"args": map[string]any{
		"deviceId": "dev-1", "stateDigest": rig.digest, "sessionId": opened.Info.ID,
		"identity": map[string]any{"createdAtUnixNano": opened.Identity.CreatedAtUnixNano, "incarnation": opened.Identity.Incarnation},
	}})
	if err != nil {
		t.Fatal(err)
	}
	resumeResponse := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalResume, string(goodResume))
	if !resumeResponse.OK {
		t.Fatalf("resume failed: %+v", resumeResponse.Error)
	}
	var resumed deviceTerminalOpenResponse
	if err := json.Unmarshal(resumeResponse.Data, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.ChannelID == opened.ChannelID {
		t.Fatal("resume must allocate a fresh channel id")
	}
	if begin := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalBegin, `{"args":{"channelId":"`+resumed.ChannelID+`"}}`); !begin.OK {
		t.Fatalf("begin after resume failed: %+v", begin.Error)
	}
	waitOutputContains(t, rig, resumed.ChannelID, marker)

	// killSession 在独立控制桥接上按 id+身份结束会话。
	killPayload, err := json.Marshal(map[string]any{"args": map[string]any{
		"deviceId": "dev-1", "stateDigest": rig.digest, "sessionId": opened.Info.ID,
		"identity": map[string]any{"createdAtUnixNano": opened.Identity.CreatedAtUnixNano, "incarnation": opened.Identity.Incarnation},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalKillSession, string(killPayload)); !response.OK {
		t.Fatalf("killSession failed: %+v", response.Error)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(rig.listSessions(t)) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("session still listed after killSession")
}

func TestDeviceTerminalExitAndCleanClose(t *testing.T) {
	rig := newDeviceTerminalTestRig(t)
	opened := mustOpenDeviceTerminal(t, rig, rig.digest, 80, 24)
	waitShellReady(t, rig, opened.ChannelID)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		sendDeviceTerminalInput(t, rig, opened.ChannelID, []byte("exit 42\n"))
		if exitSeenWithin(rig, opened.ChannelID, 700*time.Millisecond) {
			break
		}
	}
	exitEvent := rig.events.waitFor(t, func(event ipc.Event) bool {
		if event.Event != TopicDeviceTerminalExit {
			return false
		}
		exit, ok := event.Payload.(DeviceTerminalExitEvent)
		return ok && exit.ChannelID == opened.ChannelID
	}, "exit event")
	if exit := exitEvent.Payload.(DeviceTerminalExitEvent); exit.Code != 42 || exit.Signal != "" {
		t.Fatalf("exit event = %+v, want code 42", exit)
	}
	closedEvent := rig.events.waitFor(t, func(event ipc.Event) bool {
		if event.Event != TopicDeviceTerminalClosed {
			return false
		}
		closed, ok := event.Payload.(DeviceTerminalClosedEvent)
		return ok && closed.ChannelID == opened.ChannelID
	}, "closed event")
	if closed := closedEvent.Payload.(DeviceTerminalClosedEvent); closed.Code != "" || closed.Message != "" {
		t.Fatalf("closed after exit must be clean, got %+v", closed)
	}
	if response := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalInput, `{"args":{"channelId":"`+opened.ChannelID+`","data":[65]}}`); response.OK {
		t.Fatal("channel must be removed after the session exited")
	}
}

func exitSeenWithin(rig *deviceTerminalTestRig, channelID string, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		rig.events.mu.Lock()
		found := false
		for _, event := range rig.events.events {
			if event.Event != TopicDeviceTerminalExit {
				continue
			}
			if exit, ok := event.Payload.(DeviceTerminalExitEvent); ok && exit.ChannelID == channelID {
				found = true
				break
			}
		}
		rig.events.mu.Unlock()
		if found {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestDeviceTerminalKill(t *testing.T) {
	rig := newDeviceTerminalTestRig(t)
	opened := mustOpenDeviceTerminal(t, rig, rig.digest, 80, 24)
	if response := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalKill, `{"args":{"channelId":"`+opened.ChannelID+`"}}`); !response.OK {
		t.Fatalf("kill failed: %+v", response.Error)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(rig.listSessions(t)) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("session still listed after kill")
}

func TestDeviceTerminalDisconnectCleanup(t *testing.T) {
	rig := newDeviceTerminalTestRig(t)
	opened := mustOpenDeviceTerminal(t, rig, rig.digest, 80, 24)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rig.server.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	closedEvent := rig.events.waitFor(t, func(event ipc.Event) bool {
		if event.Event != TopicDeviceTerminalClosed {
			return false
		}
		closed, ok := event.Payload.(DeviceTerminalClosedEvent)
		return ok && closed.ChannelID == opened.ChannelID && closed.Code != ""
	}, "disconnected closed event")
	if closed := closedEvent.Payload.(DeviceTerminalClosedEvent); closed.Code != "disconnected" {
		t.Fatalf("closed code = %q, want disconnected", closed.Code)
	}
	if response := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalInput, `{"args":{"channelId":"`+opened.ChannelID+`","data":[65]}}`); response.OK {
		t.Fatal("channel must be removed after a disconnect")
	}
}

func TestDeviceTerminalOpenStateMismatch(t *testing.T) {
	rig := newDeviceTerminalTestRig(t)
	payload, err := json.Marshal(map[string]any{"args": map[string]any{
		"deviceId": "dev-1", "stateDigest": "deadbeef", "cols": 80, "rows": 24,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalOpen, string(payload)); response.OK {
		t.Fatal("open with a wrong digest must fail")
	}
	if sessions := rig.listSessions(t); len(sessions) != 0 {
		t.Fatalf("failed open must not leave sessions, got %+v", sessions)
	}
}

func TestDeviceTerminalOpenAttachFailureReapsCreate(t *testing.T) {
	var dials atomic.Int64
	rig := newDeviceTerminalTestRigWithDial(t, func(next deviceTerminalDial) deviceTerminalDial {
		return func(ctx context.Context, deviceID string) (net.Conn, error) {
			if dials.Add(1) == 2 {
				return nil, errors.New("attach dial boom")
			}
			return next(ctx, deviceID)
		}
	})
	payload, err := json.Marshal(map[string]any{"args": map[string]any{
		"deviceId": "dev-1", "stateDigest": rig.digest, "cols": 80, "rows": 24,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalOpen, string(payload)); response.OK {
		t.Fatal("open must fail when the attach dial fails")
	}
	if sessions := rig.listSessions(t); len(sessions) != 0 {
		t.Fatalf("attach failure must reap the created session, got %+v", sessions)
	}
}

func TestDeviceTerminalValidation(t *testing.T) {
	rig := newDeviceTerminalTestRig(t)
	for _, command := range []string{
		CommandDeviceTerminalOpen, CommandDeviceTerminalResume, CommandDeviceTerminalBegin,
		CommandDeviceTerminalInput, CommandDeviceTerminalResize, CommandDeviceTerminalKill,
		CommandDeviceTerminalKillSession, CommandDeviceTerminalClose,
	} {
		if response := dispatchDeviceTerminal(t, rig, command, `{"args":{}}`); response.OK {
			t.Fatalf("%s with empty args must fail", command)
		}
	}
	if response := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalInput, `{"args":{"channelId":"missing","data":[65]}}`); response.OK {
		t.Fatal("input on an unknown channel must fail")
	}
	if response := dispatchDeviceTerminal(t, rig, CommandDeviceTerminalResume, `{"args":{"deviceId":"dev-1","stateDigest":"x","sessionId":"s","identity":{"createdAtUnixNano":"not-a-number","incarnation":"i"}}}`); response.OK {
		t.Fatal("resume with a malformed identity must fail")
	}
}
