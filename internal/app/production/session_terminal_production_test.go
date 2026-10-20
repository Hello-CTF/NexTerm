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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

func TestProductionLocalTerminalMultiClientAttachAndControl(t *testing.T) {
	dataDir := durableTestDataDir(t)
	factory := &bridgeTestFactory{}
	production := newTerminalTestProduction(t, dataDir, factory, nil)
	connected := connectLocalTerminalTest(t, production)
	channelA := "local-multi-a"
	channelB := "local-multi-b"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelA, "client-a")
	tabID := requireAttachedTabID(t, attachResponse)
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

func TestProductionTerminalRecordAndExportResolveStagedPaths(t *testing.T) {
	factory := &bridgeTestFactory{}
	staged := &fakeStagedBlobResolver{dir: t.TempDir()}
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir: t.TempDir(), Desktop: false, StagedBlobs: staged,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connected := connectLocalTerminalTest(t, production)
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "staged-record", "client-a")
	tabID := requireAttachedTabID(t, attachResponse)
	stream := factory.at("staged-record", 0)

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_record_start", `{"tabId":"`+tabID+`","path":"staged-id/rec.log"}`, "", "client-a"))
	writeDurableTestCommand(t, production, tabID, "staged-record-marker", "client-a")
	waitForProductionOutput(t, stream, "staged-record-marker")
	stopResponse := dispatchDurableTest(t, production, "terminal_record_stop", `{"tabId":"`+tabID+`"}`, "", "client-a")
	var recorded uint64
	requireStoreTestResponse(t, stopResponse, &recorded)
	if recorded == 0 {
		t.Fatal("recorded bytes = 0")
	}
	recordedData, err := os.ReadFile(filepath.Join(staged.dir, "rec.log"))
	if err != nil || !bytes.Contains(recordedData, []byte("staged-record-marker")) {
		t.Fatalf("recorded staged file = %q, %v", recordedData, err)
	}

	exportResponse := dispatchDurableTest(t, production, "terminal_export_log", `{"tabId":"`+tabID+`","path":"staged-id/export.log","maxBytes":262144}`, "", "client-a")
	var exported uint64
	requireStoreTestResponse(t, exportResponse, &exported)
	exportedData, err := os.ReadFile(filepath.Join(staged.dir, "export.log"))
	if err != nil || !bytes.Contains(exportedData, []byte("staged-record-marker")) {
		t.Fatalf("exported staged file = %q, %v", exportedData, err)
	}

	rejected := dispatchDurableTest(t, production, "terminal_record_start", `{"tabId":"`+tabID+`","path":"/tmp/rec.log"}`, "", "client-a")
	if rejected.OK || rejected.Error == nil || rejected.Error.Code != ipc.CodeBadParam {
		t.Fatalf("absolute record path = %+v, want bad_param", rejected)
	}
	unknown := dispatchDurableTest(t, production, "terminal_export_log", `{"tabId":"`+tabID+`","path":"other-id/export.log"}`, "", "client-a")
	if unknown.OK || unknown.Error == nil || unknown.Error.Code != ipc.CodeNotFound {
		t.Fatalf("unknown staged ref = %+v, want not_found", unknown)
	}
}

func TestProductionTerminalRecordStopIPCContract(t *testing.T) {
	for _, test := range []struct {
		name    string
		bytes   uint64
		stopErr error
	}{
		{name: "success returns number", bytes: 17},
		{name: "failure returns IPC error", bytes: 9, stopErr: errors.New("recording disk full")},
	} {
		t.Run(test.name, func(t *testing.T) {
			recordingTerminal := &recordingIPCStopTerminal{bytes: test.bytes, err: test.stopErr}
			manager := session.NewManager(session.Config{Terminals: session.TerminalFactoryFunc(func(session.TerminalConfig) (session.TerminalState, error) {
				return recordingTerminal, nil
			})})
			t.Cleanup(func() { _ = manager.Close() })
			tab, err := manager.RegisterRemoteSession(t.Context(), session.RemoteSessionOptions{OwnerID: "recording-owner"})
			if err != nil {
				t.Fatal(err)
			}
			service := newTerminalCommandService(nil, manager, nil, nil, nil, nil, nil, nil, false)
			dispatcher := ipc.NewDispatcher()
			if err := service.registerSession(dispatcher); err != nil {
				t.Fatal(err)
			}
			response := dispatchStoreTest(dispatcher, "terminal_record_stop", `{"tabId":"`+tab.ID+`"}`)
			if test.stopErr == nil {
				var recorded uint64
				requireStoreTestResponse(t, response, &recorded)
				if recorded != test.bytes {
					t.Fatalf("recorded = %d, want %d", recorded, test.bytes)
				}
				return
			}
			if response.OK || response.Error == nil || response.Error.Code != ipc.CodeInternal {
				t.Fatalf("failed stop = %+v, want internal IPC error", response)
			}
			if !errors.Is(response.Error, test.stopErr) {
				t.Fatalf("stop error = %v, want cause %v", response.Error, test.stopErr)
			}
		})
	}
}

func TestProductionTerminalRecordStatusIPCContract(t *testing.T) {
	recordingTerminal := &recordingIPCStopTerminal{}
	manager := session.NewManager(session.Config{Terminals: session.TerminalFactoryFunc(func(session.TerminalConfig) (session.TerminalState, error) {
		return recordingTerminal, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	tab, err := manager.RegisterRemoteSession(t.Context(), session.RemoteSessionOptions{OwnerID: "recording-owner"})
	if err != nil {
		t.Fatal(err)
	}
	want := terminal.RecordingStatus{
		TabID: tab.ID, State: terminal.RecordingFailed, Bytes: 29,
		Path: "/tmp/failed.log", SafeError: "disk full", Generation: 3,
	}
	recordingTerminal.status = want
	service := newTerminalCommandService(nil, manager, nil, nil, nil, nil, nil, nil, false)
	dispatcher := ipc.NewDispatcher()
	if err := service.registerSession(dispatcher); err != nil {
		t.Fatal(err)
	}
	response := dispatchStoreTest(dispatcher, "terminal_record_status", `{"tabId":"`+tab.ID+`"}`)
	var got terminal.RecordingStatus
	requireStoreTestResponse(t, response, &got)
	if got != want {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
}

type recordingIPCStopTerminal struct {
	bytes  uint64
	err    error
	status terminal.RecordingStatus
}

func (t *recordingIPCStopTerminal) StopRecording() (uint64, error)            { return t.bytes, t.err }
func (t *recordingIPCStopTerminal) RecordingStatus() terminal.RecordingStatus { return t.status }
func (t *recordingIPCStopTerminal) Feed([]byte)                               {}
func (t *recordingIPCStopTerminal) Dump(int) []byte                           { return nil }
func (t *recordingIPCStopTerminal) Resize(int, int) error                     { return nil }
func (t *recordingIPCStopTerminal) SetResponseHandler(func([]byte))           {}
func (t *recordingIPCStopTerminal) Close()                                    {}

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
	if attached.Shell != string(session.ShellUnknown) {
		t.Fatalf("reattached docker shell = %q, want unknown", attached.Shell)
	}
	waitForProductionOutput(t, factory.at(reattachChannel, 0), "docker-background-output")

	writeDurableTestInput(t, production, streamID, "docker-takeover-input", "client-a")
	if !execSession.received("docker-takeover-input\r") {
		t.Fatal("terminal_write did not reach the docker exec session after reattach")
	}

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+streamID+`","clientId":"client-a"}`, "", "client-a"))
}

func TestProductionDockerExecClosedElsewhereCanBeRemoved(t *testing.T) {
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

	execResponse := dispatchDurableTest(t, production, "docker_exec_attach", `{"args":{"sessionId":"`+connected.ID+`","containerId":"container-1","cols":80,"rows":24}}`, "docker-closed-elsewhere", "client-a")
	var streamID string
	requireStoreTestResponse(t, execResponse, &streamID)
	if err := dockerService.CloseStream(streamID); err != nil {
		t.Fatal(err)
	}

	response := dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+streamID+`"}`, "", "client-a")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("closing a stream already ended elsewhere = %+v, want not_found", response)
	}
}

func TestProductionTerminalWriteIsolatesTabsForBroadcast(t *testing.T) {
	dataDir := durableTestDataDir(t)
	factory := &bridgeTestFactory{}
	production := newTerminalTestProduction(t, dataDir, factory, nil)
	connected := connectLocalTerminalTest(t, production)

	channelA := "broadcast-tab-a"
	channelB := "broadcast-tab-b"
	attachA := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelA, "client-a")
	tabA := requireAttachedTabID(t, attachA)
	attachB := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelB, "client-a")
	tabB := requireAttachedTabID(t, attachB)
	if tabA == tabB {
		t.Fatalf("two attaches returned the same tab %s", tabA)
	}
	streamA := factory.at(channelA, 0)
	streamB := factory.at(channelB, 0)

	writeDurableTestCommand(t, production, tabA, "broadcast-only-a", "client-a")
	waitForProductionOutput(t, streamA, "broadcast-only-a")
	if bytes.Contains(streamB.output(), []byte("broadcast-only-a")) {
		t.Fatalf("write to tab A leaked to tab B: %q", streamB.output())
	}

	writeDurableTestCommand(t, production, tabB, "broadcast-only-b", "client-a")
	waitForProductionOutput(t, streamB, "broadcast-only-b")
	if bytes.Contains(streamA.output(), []byte("broadcast-only-b")) {
		t.Fatalf("write to tab B leaked to tab A: %q", streamA.output())
	}

	attachA2 := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabA+`","replayBytes":262144}`, "broadcast-tab-a2", "client-b")
	var info attachedTabDTO
	requireStoreTestResponse(t, attachA2, &info)
	if info.Controller == nil || *info.Controller != "client-a" {
		t.Fatalf("controller after second attach = %+v, want client-a", info.Controller)
	}
	writeForeign := dispatchDurableTest(t, production, "terminal_write", `{"args":{"tabId":"`+tabA+`","data":[101,99,104,111,13],"clientId":"client-b"}}`, "", "client-b")
	if writeForeign.OK || writeForeign.Error == nil || writeForeign.Error.Code != ipc.CodeNotController {
		t.Fatalf("write from non-controller = %+v, want not_controller", writeForeign)
	}

	writeDurableTestInput(t, production, tabB, "exit", "client-a")
	deadline := time.Now().Add(3 * time.Second)
	for {
		listResponse := dispatchDurableTest(t, production, "terminal_list", `null`, "", "")
		var live []liveTabDTO
		requireStoreTestResponse(t, listResponse, &live)
		exited := false
		for _, candidate := range live {
			if candidate.TabID == tabB && candidate.Exited {
				exited = true
			}
		}
		if exited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tab B never marked exited: %+v", live)
		}
		time.Sleep(20 * time.Millisecond)
	}
	writeExited := dispatchDurableTest(t, production, "terminal_write", `{"args":{"tabId":"`+tabB+`","data":[101,99,104,111,13],"clientId":"client-a"}}`, "", "client-a")
	if writeExited.OK || writeExited.Error == nil || writeExited.Error.Code != ipc.CodeDisconnected {
		t.Fatalf("write to exited tab = %+v, want disconnected", writeExited)
	}
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

type productionAuthorizationFixture struct {
	production   *Production
	manager      *session.Manager
	owner        context.Context
	peer         context.Context
	other        context.Context
	admin        context.Context
	ownerSession *session.Session
	otherSession *session.Session
	ownerTab     session.TabInfo
	otherTab     session.TabInfo
}

type productionAuthorizationActor struct {
	name     string
	ctx      context.Context
	clientID string
	allowed  bool
}

func newProductionAuthorizationFixture(t *testing.T, open bool) *productionAuthorizationFixture {
	t.Helper()
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := session.NewManager(session.Config{
		Connector: connector, OpenAccess: open,
		DurableResolver: &recordingDurableResolver{provider: newRemoteDaemonFixture(t).provider},
	})
	factory := &bridgeTestFactory{}
	production, err := NewProductionWithServices(Config{
		Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
	}, ProductionServices{
		Sessions: manager, stagedBlobs: &fakeStagedBlobResolver{dir: t.TempDir()}, desktop: open,
	})
	if err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		_ = production.Shutdown(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })

	fixture := &productionAuthorizationFixture{
		production: production,
		manager:    manager,
		owner:      ipc.WithUserID(t.Context(), "owner-a"),
		peer:       ipc.WithUserID(t.Context(), "owner-a"),
		other:      ipc.WithUserID(t.Context(), "owner-b"),
		admin:      ipc.WithRole(ipc.WithUserID(t.Context(), "admin"), "superadmin"),
	}
	ownerConnect, otherConnect := fixture.owner, fixture.other
	if open {
		ownerConnect, otherConnect = t.Context(), t.Context()
	}
	fixture.ownerSession = fixture.connect(t, ownerConnect, "session-a", "owner-a")
	fixture.otherSession = fixture.connect(t, otherConnect, "session-b", "owner-b")
	fixture.ownerTab = fixture.openTab(t, ownerConnect, fixture.ownerSession.ID, "owner-client", "owner-channel")
	fixture.otherTab = fixture.openTab(t, otherConnect, fixture.otherSession.ID, "other-client", "other-channel")
	return fixture
}

func (f *productionAuthorizationFixture) connect(t *testing.T, ctx context.Context, id, name string) *session.Session {
	t.Helper()
	connected, err := f.manager.Connect(ctx, session.Asset{ID: id, Name: name, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	return connected
}

func (f *productionAuthorizationFixture) openTab(t *testing.T, ctx context.Context, sessionID, clientID, channelID string) session.TabInfo {
	t.Helper()
	tab, err := f.manager.OpenTab(ctx, session.OpenTabOptions{
		SessionID: sessionID, ClientID: clientID, ChannelID: channelID, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func (f *productionAuthorizationFixture) actor(name string) productionAuthorizationActor {
	switch name {
	case "owner":
		return productionAuthorizationActor{name: name, ctx: f.owner, clientID: "owner-client", allowed: true}
	case "same-account-second-client":
		return productionAuthorizationActor{name: name, ctx: f.peer, clientID: "peer-client", allowed: true}
	case "other-account":
		return productionAuthorizationActor{name: name, ctx: f.other, clientID: "owner-client"}
	case "superadmin":
		return productionAuthorizationActor{name: name, ctx: f.admin, clientID: "owner-client"}
	case "open":
		return productionAuthorizationActor{name: name, ctx: context.Background(), clientID: "owner-client", allowed: true}
	default:
		panic("unknown authorization actor " + name)
	}
}

func (f *productionAuthorizationFixture) dispatch(actor productionAuthorizationActor, command, args, channelID string) ipc.Response {
	return f.production.Dispatcher.Dispatch(actor.ctx, ipc.Request{
		Command: command, Args: json.RawMessage(args), Channel: ipc.ChannelRef{ID: channelID}, ClientID: actor.clientID,
	}, f.production.Environment(actor.clientID))
}

func requireProductionAuthorization(t *testing.T, response ipc.Response, allowed bool) {
	t.Helper()
	if allowed {
		if !response.OK {
			t.Fatalf("authorized request failed: %+v", response.Error)
		}
		return
	}
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("cross-account request = %+v, want forbidden", response)
	}
}

var productionAuthorizationActorNames = []string{"owner", "same-account-second-client", "other-account", "superadmin", "open"}

func TestProductionSessionAndTerminalListsAreOwnerScoped(t *testing.T) {
	for _, actorName := range productionAuthorizationActorNames {
		t.Run(actorName, func(t *testing.T) {
			fixture := newProductionAuthorizationFixture(t, actorName == "open")
			actor := fixture.actor(actorName)
			sessionResponse := fixture.dispatch(actor, "session_list", `null`, "")
			var sessions []sessionInfoDTO
			requireStoreTestResponse(t, sessionResponse, &sessions)
			terminalResponse := fixture.dispatch(actor, "terminal_list", `null`, "")
			var tabs []liveTabDTO
			requireStoreTestResponse(t, terminalResponse, &tabs)

			wantSessions := map[string]bool{}
			wantTabs := map[string]bool{}
			switch actorName {
			case "owner", "same-account-second-client":
				wantSessions[fixture.ownerSession.ID] = true
				wantTabs[fixture.ownerTab.ID] = true
			case "other-account":
				wantSessions[fixture.otherSession.ID] = true
				wantTabs[fixture.otherTab.ID] = true
			case "open":
				wantSessions[fixture.ownerSession.ID] = true
				wantSessions[fixture.otherSession.ID] = true
				wantTabs[fixture.ownerTab.ID] = true
				wantTabs[fixture.otherTab.ID] = true
			}
			if len(sessions) != len(wantSessions) || len(tabs) != len(wantTabs) {
				t.Fatalf("visible resources: sessions=%+v tabs=%+v, want sessions=%v tabs=%v", sessions, tabs, wantSessions, wantTabs)
			}
			for _, info := range sessions {
				if !wantSessions[info.ID] {
					t.Fatalf("unauthorized session %s visible: %+v", info.ID, sessions)
				}
			}
			for _, info := range tabs {
				if !wantTabs[info.TabID] {
					t.Fatalf("unauthorized tab %s visible: %+v", info.TabID, tabs)
				}
			}
		})
	}
}

func TestProductionTerminalRequestsEnforceAccountOwnership(t *testing.T) {
	operations := []struct {
		name       string
		controller bool
		invoke     func(*productionAuthorizationFixture, productionAuthorizationActor) []ipc.Response
	}{
		{name: "read", invoke: func(f *productionAuthorizationFixture, actor productionAuthorizationActor) []ipc.Response {
			args := `{"tabId":"` + f.ownerTab.ID + `","n":10,"maxBytes":1024}`
			return []ipc.Response{
				f.dispatch(actor, "terminal_snapshot", args, ""),
				f.dispatch(actor, "terminal_screen_text", args, ""),
				f.dispatch(actor, "terminal_tail", args, ""),
				f.dispatch(actor, "terminal_dump", args, ""),
			}
		}},
		{name: "attach-and-create", invoke: func(f *productionAuthorizationFixture, actor productionAuthorizationActor) []ipc.Response {
			return []ipc.Response{
				f.dispatch(actor, "terminal_attach", `{"sessionId":"`+f.ownerSession.ID+`","cols":80,"rows":24}`, "attach-session-"+actor.name),
				f.dispatch(actor, "terminal_attach_tab", `{"tabId":"`+f.ownerTab.ID+`","replayBytes":1024}`, "attach-tab-"+actor.name),
				f.dispatch(actor, "terminal_create_tab", `{"sessionId":"`+f.ownerSession.ID+`","title":"created","cols":80,"rows":24}`, ""),
			}
		}},
		{name: "write", controller: true, invoke: func(f *productionAuthorizationFixture, actor productionAuthorizationActor) []ipc.Response {
			return []ipc.Response{f.dispatch(actor, "terminal_write", `{"args":{"tabId":"`+f.ownerTab.ID+`","data":[120],"clientId":"`+actor.clientID+`"}}`, "")}
		}},
		{name: "claim-and-release", invoke: func(f *productionAuthorizationFixture, actor productionAuthorizationActor) []ipc.Response {
			clientID := actor.name + "-controller"
			args := `{"tabId":"` + f.ownerTab.ID + `","clientId":"` + clientID + `"}`
			return []ipc.Response{
				f.dispatch(actor, "terminal_claim", args, ""),
				f.dispatch(actor, "terminal_release", args, ""),
			}
		}},
		{name: "resize-and-flush", controller: true, invoke: func(f *productionAuthorizationFixture, actor productionAuthorizationActor) []ipc.Response {
			return []ipc.Response{
				f.dispatch(actor, "terminal_resize", `{"tabId":"`+f.ownerTab.ID+`","clientId":"`+actor.clientID+`","cols":100,"rows":30}`, ""),
				f.dispatch(actor, "terminal_resize_flush", `{"tabId":"`+f.ownerTab.ID+`","clientId":"`+actor.clientID+`"}`, ""),
			}
		}},
		{name: "rename", invoke: func(f *productionAuthorizationFixture, actor productionAuthorizationActor) []ipc.Response {
			return []ipc.Response{f.dispatch(actor, "terminal_rename_tab", `{"tabId":"`+f.ownerTab.ID+`","title":"renamed"}`, "")}
		}},
		{name: "detach", invoke: func(f *productionAuthorizationFixture, actor productionAuthorizationActor) []ipc.Response {
			return []ipc.Response{f.dispatch(actor, "terminal_detach", `{"tabId":"`+f.ownerTab.ID+`","channelId":"owner-channel"}`, "")}
		}},
		{name: "record", invoke: func(f *productionAuthorizationFixture, actor productionAuthorizationActor) []ipc.Response {
			return []ipc.Response{
				f.dispatch(actor, "terminal_record_start", `{"tabId":"`+f.ownerTab.ID+`","path":"staged-id/record.log"}`, ""),
				f.dispatch(actor, "terminal_record_stop", `{"tabId":"`+f.ownerTab.ID+`"}`, ""),
				f.dispatch(actor, "terminal_record_status", `{"tabId":"`+f.ownerTab.ID+`"}`, ""),
				f.dispatch(actor, "terminal_export_log", `{"tabId":"`+f.ownerTab.ID+`","path":"staged-id/export.log","maxBytes":1024}`, ""),
			}
		}},
		{name: "visibility-and-encoding", invoke: func(f *productionAuthorizationFixture, actor productionAuthorizationActor) []ipc.Response {
			return []ipc.Response{
				f.dispatch(actor, "terminal_set_visible", `{"tabId":"`+f.ownerTab.ID+`","visible":true}`, ""),
				f.dispatch(actor, "terminal_switch_encoding", `{"tabId":"`+f.ownerTab.ID+`","encoding":"utf8"}`, ""),
			}
		}},
		{name: "close", invoke: func(f *productionAuthorizationFixture, actor productionAuthorizationActor) []ipc.Response {
			return []ipc.Response{f.dispatch(actor, "terminal_close_tab", `{"tabId":"`+f.ownerTab.ID+`","clientId":"`+actor.clientID+`"}`, "")}
		}},
		{name: "reconnect", invoke: func(f *productionAuthorizationFixture, actor productionAuthorizationActor) []ipc.Response {
			return []ipc.Response{f.dispatch(actor, "session_reconnect", `{"sessionId":"`+f.ownerSession.ID+`"}`, "")}
		}},
		{name: "disconnect", invoke: func(f *productionAuthorizationFixture, actor productionAuthorizationActor) []ipc.Response {
			return []ipc.Response{f.dispatch(actor, "session_disconnect", `{"sessionId":"`+f.ownerSession.ID+`"}`, "")}
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			for _, actorName := range productionAuthorizationActorNames {
				t.Run(actorName, func(t *testing.T) {
					fixture := newProductionAuthorizationFixture(t, actorName == "open")
					actor := fixture.actor(actorName)
					if operation.controller && actorName == "same-account-second-client" {
						claim := fixture.dispatch(actor, "terminal_claim", `{"tabId":"`+fixture.ownerTab.ID+`","clientId":"`+actor.clientID+`"}`, "")
						requireProductionAuthorization(t, claim, true)
					}
					for _, response := range operation.invoke(fixture, actor) {
						requireProductionAuthorization(t, response, actor.allowed)
					}
				})
			}
		})
	}
}
