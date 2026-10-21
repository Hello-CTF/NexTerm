//go:build darwin || linux

package production

import (
	"bytes"
	"context"
	"net"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

type deviceBridgeFaultInjector struct {
	dial DeviceBridgeDialFunc

	mu          sync.Mutex
	active      map[*deviceBridgeFaultConn]struct{}
	blocked     bool
	dialStarted chan struct{}
	startOnce   sync.Once
	release     chan struct{}
	releaseOnce sync.Once
}

func newDeviceBridgeFaultInjector(dial DeviceBridgeDialFunc) *deviceBridgeFaultInjector {
	return &deviceBridgeFaultInjector{
		dial: dial, active: make(map[*deviceBridgeFaultConn]struct{}),
		dialStarted: make(chan struct{}), release: make(chan struct{}),
	}
}

func (f *deviceBridgeFaultInjector) Dial(ctx context.Context, deviceID, purpose string) (net.Conn, error) {
	f.mu.Lock()
	blocked := f.blocked
	f.mu.Unlock()
	if blocked {
		f.startOnce.Do(func() { close(f.dialStarted) })
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	conn, err := f.dial(ctx, deviceID, purpose)
	if err != nil || conn == nil {
		return conn, err
	}
	fault := &deviceBridgeFaultConn{Conn: conn, owner: f}
	f.mu.Lock()
	f.active[fault] = struct{}{}
	f.mu.Unlock()
	return fault, nil
}

func (f *deviceBridgeFaultInjector) blockReconnects() {
	f.mu.Lock()
	f.blocked = true
	f.mu.Unlock()
}

func (f *deviceBridgeFaultInjector) releaseReconnects() {
	f.releaseOnce.Do(func() { close(f.release) })
}

func (f *deviceBridgeFaultInjector) dropActive(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	connections := make([]*deviceBridgeFaultConn, 0, len(f.active))
	for conn := range f.active {
		connections = append(connections, conn)
	}
	f.mu.Unlock()
	if len(connections) != 1 {
		t.Fatalf("active terminal bridges before fault = %d; want exactly one attach", len(connections))
	}
	if err := connections[0].Close(); err != nil {
		t.Fatal(err)
	}
}

func (f *deviceBridgeFaultInjector) close() {
	f.releaseReconnects()
	f.mu.Lock()
	connections := make([]*deviceBridgeFaultConn, 0, len(f.active))
	for conn := range f.active {
		connections = append(connections, conn)
	}
	f.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
}

type deviceBridgeFaultConn struct {
	net.Conn
	owner *deviceBridgeFaultInjector
	once  sync.Once
	err   error
}

func (c *deviceBridgeFaultConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.owner.mu.Lock()
		delete(c.owner.active, c)
		c.owner.mu.Unlock()
	})
	return c.err
}

func TestProductionDeviceBridgeFailureReconnectsLiveDurableProcess(t *testing.T) {
	rig := newDeviceIPCTestRig(t)
	config := rig.connector(&sync.Map{})
	injector := newDeviceBridgeFaultInjector(config.Dial)
	config.Dial = injector.Dial
	t.Cleanup(injector.close)

	var eventMu sync.Mutex
	var terminalEvents []ipc.Event
	events := ipc.EmitterFunc(func(_ context.Context, event ipc.Event) error {
		if event.Event == ipc.TopicTerminalExit || event.Event == ipc.TopicTerminalControl {
			eventMu.Lock()
			terminalEvents = append(terminalEvents, event)
			eventMu.Unlock()
		}
		return nil
	})
	factory := &bridgeTestFactory{}
	production := newTerminalTestProduction(t, durableTestDataDir(t), factory, events)
	production.Services.SetDeviceConnector(config)

	connectResponse := dispatchDurableTest(t, production, "session_connect_device", `{"args":{"deviceId":"dev-reconnect","name":"build-box"}}`, "", "client-a")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectResponse, &connected)
	channelID := "device-auto-reconnect"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	tabID := requireAttachedTabID(t, attachResponse)
	stream := factory.at(channelID, 0)
	writeDurableTestCommand(t, production, tabID, "device-before-fault", "client-a")
	waitForProductionOutput(t, stream, "device-before-fault")
	writeDurableTestInput(t, production, tabID, `printf 'device-pid-before:%s\n' "$$"`, "client-a")
	beforePID := waitForDeviceTestPID(t, stream, "device-pid-before")
	before := deviceTestDurableInfo(t, rig, tabID)
	if before.Dead {
		t.Fatalf("durable process is dead before fault: %+v", before)
	}

	injector.blockReconnects()
	injector.dropActive(t)
	select {
	case <-injector.dialStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("active attach failure did not trigger a reconnect dial")
	}
	deviceTestWriteDuringDisconnect(t, rig, tabID, "device-disconnect-marker")
	injector.releaseReconnects()
	waitForSessionStatus(t, production, connected.ID, "connected")

	managed, err := production.Services.Sessions.Session(connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := managed.Info(); got.ID != connected.ID || got.Generation != 2 {
		t.Fatalf("automatically recovered session = %+v; want same session at generation 2", got)
	}
	tab, err := production.Services.Sessions.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tab.Info(); got.ID != tabID || got.SessionID != connected.ID || !got.Durable || got.Exited {
		t.Fatalf("automatically recovered tab = %+v; want same live durable tab", got)
	}
	waitForProductionOutput(t, stream, "device-disconnect-marker")
	writeDurableTestInput(t, production, tabID, `printf 'device-pid-after:%s\n' "$$"`, "client-a")
	afterPID := waitForDeviceTestPID(t, stream, "device-pid-after")
	if afterPID != beforePID {
		t.Fatalf("shell PID changed across bridge reconnect: before=%s after=%s", beforePID, afterPID)
	}
	after := deviceTestDurableInfo(t, rig, tabID)
	if after.Dead || after.ID != before.ID || after.Incarnation != before.Incarnation || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("durable identity changed across bridge reconnect: before=%+v after=%+v", before, after)
	}
	raw, err := production.Services.Sessions.RawDump(tabID, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(raw), "device-disconnect-marker"); count != 1 {
		t.Fatalf("disconnect-window output appears %d times in terminal; want exactly once: %q", count, raw)
	}
	if count := strings.Count(string(stream.output()), "device-disconnect-marker"); count != 1 {
		t.Fatalf("disconnect-window output appears %d times in client stream; want exactly once", count)
	}

	eventMu.Lock()
	defer eventMu.Unlock()
	for _, event := range terminalEvents {
		if event.Event == ipc.TopicTerminalExit {
			t.Fatalf("bridge failure emitted terminal exit: %+v", event.Payload)
		}
		if control, ok := event.Payload.(session.ControlEvent); ok && control.TabID == tabID && control.Exited {
			t.Fatalf("bridge failure emitted exited control: %+v", control)
		}
	}
}

func deviceTestWriteDuringDisconnect(t *testing.T, rig *deviceIPCTestRig, tabID, marker string) {
	t.Helper()
	client := supervisor.NewClient(rig.endpoint, rig.stateDir)
	stream, err := client.Attach(t.Context(), tabID, nil)
	if err != nil {
		t.Fatalf("attach independent helper stream during disconnect: %v", err)
	}
	defer func() { _ = stream.Close() }()
	command := "printf 'device-disconnect-%s\\n' 'marker'\r"
	if _, err := stream.Write([]byte(command)); err != nil {
		t.Fatalf("write through surviving helper during disconnect: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	buffer := make([]byte, 4096)
	var output strings.Builder
	for !strings.Contains(output.String(), marker) {
		if time.Now().After(deadline) {
			t.Fatalf("helper did not record disconnect-window output: %q", output.String())
		}
		count, err := stream.Read(buffer)
		if count > 0 {
			output.Write(buffer[:count])
		}
		if err != nil {
			t.Fatalf("read surviving helper during disconnect: %v", err)
		}
	}
}

func deviceTestDurableInfo(t *testing.T, rig *deviceIPCTestRig, tabID string) supervisor.Info {
	t.Helper()
	client := supervisor.NewClient(rig.endpoint, rig.stateDir)
	infos, err := client.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.ID == tabID {
			return info
		}
	}
	t.Fatalf("durable session %s is missing from surviving helper: %+v", tabID, infos)
	return supervisor.Info{}
}

func TestDeviceTestPIDOutputIgnoresCommandEcho(t *testing.T) {
	const marker = "device-pid-before"
	echo := []byte(`printf 'device-pid-before:%s\n' "$$"` + "\r\n")
	if pid, ok := parseDeviceTestPID(echo, marker); ok {
		t.Fatalf("command echo produced shell PID %q", pid)
	}
	output := append(echo, []byte("device-pid-before:4287\r\n")...)
	pid, ok := parseDeviceTestPID(output, marker)
	if !ok {
		t.Fatal("shell PID output did not satisfy readiness")
	}
	if pid != "4287" {
		t.Fatalf("shell PID = %q; want 4287", pid)
	}
}

func waitForDeviceTestPID(t *testing.T, stream *bridgeTestStream, marker string) string {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		stream.mu.Lock()
		output := bytes.Join(stream.frames, nil)
		pid, ok := parseDeviceTestPID(output, marker)
		changed := stream.changed
		stream.mu.Unlock()
		if ok {
			return pid
		}
		select {
		case <-changed:
		case <-timer.C:
			t.Fatalf("shell PID output for %s is missing: %q", marker, stream.output())
		}
	}
}

func parseDeviceTestPID(output []byte, marker string) (string, bool) {
	matches := regexp.MustCompile(regexp.QuoteMeta(marker) + `:([0-9]+)`).FindSubmatch(output)
	if len(matches) != 2 {
		return "", false
	}
	return string(matches[1]), true
}
