//go:build darwin || linux

package production

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/docker"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestProductionLocalTerminalMultiClientAttachAndControl(t *testing.T) {
	dataDir := durableTestDataDir(t)
	factory := &bridgeTestFactory{}
	production := newTerminalTestProduction(t, dataDir, factory, nil)
	connected := connectLocalTerminalTest(t, production)
	channelA := "local-multi-a"
	channelB := "local-multi-b"
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

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
	if tabs := production.Services.Sessions.ListTabs(); len(tabs) != 0 {
		t.Fatalf("tabs survived close: %+v", tabs)
	}
}

func TestProductionSSHTerminalStaysVolatile(t *testing.T) {
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
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })

	host := "127.0.0.1"
	port := int32(22)
	username := "root"
	assetRow, err := production.Services.Store.AssetCreate(t.Context(), store.AssetInput{
		Kind: "ssh", Name: "ssh-volatile", Host: &host, Port: &port, Username: &username,
	})
	if err != nil {
		t.Fatal(err)
	}
	connectedResponse := dispatchDurableTest(t, production, "session_connect", `{"args":{"assetId":"`+assetRow.ID+`"}}`, "ssh-volatile-channel", "client-a")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "ssh-volatile-channel", "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	tabs := production.Services.Sessions.ListTabs()
	if len(tabs) != 1 || tabs[0].ID != tabID || tabs[0].Durable {
		t.Fatalf("ssh tab = %+v, want one volatile tab", tabs)
	}

	local := connectLocalTerminalTest(t, production)
	localAttach := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+local.ID+`","cols":80,"rows":24}`, "local-volatile-attach-channel", "client-a")
	var localTabID string
	requireStoreTestResponse(t, localAttach, &localTabID)
	tabs = production.Services.Sessions.ListTabs()
	if len(tabs) != 2 {
		t.Fatalf("tabs = %+v", tabs)
	}
	for _, tab := range tabs {
		if tab.Durable {
			t.Fatalf("tab %s is durable, want volatile", tab.ID)
		}
	}
}

func TestProductionDockerExecDetachReattach(t *testing.T) {
	factory := &bridgeTestFactory{}
	execSession := newFakeStreamExecSession()
	dockerService := docker.NewService(docker.StaticBackends{
		SDKBackend:     fakeExecDockerBackend{session: execSession},
		CommandBackend: fakeExecDockerBackend{session: execSession},
	})
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir: t.TempDir(), Desktop: true,
		Docker: dockerService,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connected := connectLocalTerminalTest(t, production)

	channelID := "docker-exec-channel"
	execResponse := dispatchDurableTest(t, production, "docker_exec_attach", `{"args":{"sessionId":"`+connected.ID+`","containerId":"container-1","cols":80,"rows":24}}`, channelID, "client-a")
	var streamID string
	requireStoreTestResponse(t, execResponse, &streamID)
	if len(streamID) != 32 {
		t.Fatalf("docker stream id = %q, want 32 hex characters", streamID)
	}
	if ids.Valid(streamID) {
		t.Fatal("docker stream id unexpectedly satisfies supervisor ids.Valid")
	}

	execSession.emit("docker-first-output\n")
	waitForProductionOutput(t, factory.at(channelID, 0), "docker-first-output")

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+streamID+`","mode":"detach"}`, channelID, "client-a"))
	execSession.emit("docker-background-output\n")

	reattachChannel := "docker-exec-reattach"
	reattachResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+streamID+`","replayBytes":1024}`, reattachChannel, "client-a")
	var attached attachedTabDTO
	requireStoreTestResponse(t, reattachResponse, &attached)
	if attached.TabID != streamID || attached.SessionID != connected.ID || attached.Exited {
		t.Fatalf("reattached docker tab = %+v", attached)
	}
	waitForProductionOutput(t, factory.at(reattachChannel, 0), "docker-background-output")

	writeDurableTestInput(t, production, streamID, "docker-takeover-input", "client-a")
	if !execSession.received("docker-takeover-input\r") {
		t.Fatal("terminal_write did not reach the docker exec session after reattach")
	}

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+streamID+`","clientId":"client-a"}`, "", "client-a"))
}

type fakeStreamExecSession struct {
	mu     sync.Mutex
	buf    []byte
	writes [][]byte
	signal chan struct{}
	done   chan struct{}
	once   sync.Once
}

func newFakeStreamExecSession() *fakeStreamExecSession {
	return &fakeStreamExecSession{signal: make(chan struct{}, 1), done: make(chan struct{})}
}

func (s *fakeStreamExecSession) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if len(s.buf) > 0 {
			count := copy(p, s.buf)
			s.buf = s.buf[count:]
			s.mu.Unlock()
			return count, nil
		}
		s.mu.Unlock()
		select {
		case <-s.signal:
		case <-s.done:
			return 0, io.EOF
		}
	}
}

func (s *fakeStreamExecSession) emit(text string) {
	s.mu.Lock()
	s.buf = append(s.buf, text...)
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

func (s *fakeStreamExecSession) Write(data []byte) (int, error) {
	s.mu.Lock()
	s.writes = append(s.writes, append([]byte(nil), data...))
	s.mu.Unlock()
	return len(data), nil
}

func (s *fakeStreamExecSession) received(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Contains(string(bytes.Join(s.writes, nil)), text)
}

func (s *fakeStreamExecSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *fakeStreamExecSession) CloseWrite() error                        { return nil }
func (s *fakeStreamExecSession) Resize(context.Context, uint, uint) error { return nil }
func (s *fakeStreamExecSession) IsTTY() bool                              { return true }
func (s *fakeStreamExecSession) Wait(ctx context.Context) (int, error) {
	select {
	case <-s.done:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

type fakeExecDockerBackend struct {
	fakeDockerBackend
	session *fakeStreamExecSession
}

func (b fakeExecDockerBackend) OpenExec(context.Context, docker.ExecOptions) (docker.ExecSession, error) {
	return b.session, nil
}

func newTerminalTestProduction(t *testing.T, dataDir string, factory *bridgeTestFactory, events ipc.Emitter) *Production {
	t.Helper()
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
			Events:  events,
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

func connectLocalTerminalTest(t *testing.T, production *Production) sessionInfoDTO {
	t.Helper()
	connectedResponse := dispatchDurableTest(t, production, "session_connect_local", `null`, "", "")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)
	return connected
}
