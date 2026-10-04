//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/docker"
	"github.com/ProbiusOfficial/NexTerm/internal/durable"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
)

type fakeSSHConnector struct{}

func (fakeSSHConnector) Connect(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
	return &fakeSSHTransport{kind: asset.Kind, generation: generation}, nil
}

type fakeSSHTransport struct {
	kind       string
	generation uint64
	closed     bool
}

func (t *fakeSSHTransport) Kind() string       { return t.kind }
func (t *fakeSSHTransport) Generation() uint64 { return t.generation }
func (t *fakeSSHTransport) IsAlive() bool      { return !t.closed }
func (t *fakeSSHTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *fakeSSHTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}
func (t *fakeSSHTransport) Close() error {
	t.closed = true
	return nil
}
func (t *fakeSSHTransport) OpenPTY(_ context.Context, _ base.PTYOptions) (base.Channel, error) {
	return &fakePTYChannel{id: "fake-pty", generation: t.generation}, nil
}

type fakePTYChannel struct {
	id         string
	generation uint64
}

func (c *fakePTYChannel) Read([]byte) (int, error)                     { return 0, io.EOF }
func (c *fakePTYChannel) Write(data []byte) (int, error)               { return len(data), nil }
func (c *fakePTYChannel) Close() error                                 { return nil }
func (c *fakePTYChannel) Stderr() io.Reader                            { return nil }
func (c *fakePTYChannel) Resize(context.Context, uint32, uint32) error { return nil }
func (c *fakePTYChannel) Wait(context.Context) error                   { return nil }
func (c *fakePTYChannel) CloseWrite() error                            { return nil }
func (c *fakePTYChannel) ID() string                                   { return c.id }
func (c *fakePTYChannel) Generation() uint64                           { return c.generation }

func TestProductionMissingTmuxDoesNotBlockNonLocalTerminal(t *testing.T) {
	factory := &bridgeTestFactory{}
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir: t.TempDir(), Desktop: true, DurableBinary: "nexterm-no-such-tmux-binary",
		SupervisorStateDir: blockedSupervisorStateDir(t),
		Connector:          fakeSSHConnector{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(production.Services.DurableErr, durable.ErrUnavailable) {
		t.Fatalf("durable constructor error = %v", production.Services.DurableErr)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })

	host := "127.0.0.1"
	port := int32(22)
	username := "root"
	assetRow, err := production.Services.Store.AssetCreate(t.Context(), store.AssetInput{
		Kind: "ssh", Name: "non-local", Host: &host, Port: &port, Username: &username,
	})
	if err != nil {
		t.Fatal(err)
	}
	connectedResponse := dispatchDurableTest(t, production, "session_connect", `{"args":{"assetId":"`+assetRow.ID+`"}}`, "ssh-channel", "client-a")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)

	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "ssh-channel", "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	if tabID == "" {
		t.Fatal("non-local terminal_attach returned no tab with missing local tmux")
	}
	if tabs := production.Services.Sessions.ListTabs(); len(tabs) != 1 {
		t.Fatalf("tabs = %+v", tabs)
	}

	localResponse := dispatchDurableTest(t, production, "session_connect_local", `null`, "local-channel", "client-a")
	var local sessionInfoDTO
	requireStoreTestResponse(t, localResponse, &local)
	localAttach := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+local.ID+`","cols":80,"rows":24}`, "local-channel", "client-a")
	var localTabID string
	requireStoreTestResponse(t, localAttach, &localTabID)
	if localTabID == "" {
		t.Fatal("local attach with missing tmux returned no volatile tab")
	}
	if streams := factory.streams["local-channel"]; len(streams) != 1 {
		t.Fatalf("volatile fallback did not bridge the local channel: %+v", streams)
	}
	if tabs := production.Services.Sessions.ListTabs(); len(tabs) != 2 {
		t.Fatalf("volatile fallback tabs = %+v", tabs)
	}
}

type fakeDockerBackend struct{}

func (fakeDockerBackend) ListContainers(context.Context, bool) ([]container.Summary, error) {
	return nil, nil
}
func (fakeDockerBackend) InspectContainer(context.Context, string) (json.RawMessage, error) {
	return nil, nil
}
func (fakeDockerBackend) ContainerStats(context.Context, string) (container.StatsResponse, error) {
	return container.StatsResponse{}, nil
}
func (fakeDockerBackend) ListImages(context.Context) ([]image.Summary, error) { return nil, nil }
func (fakeDockerBackend) Action(context.Context, docker.ActionOptions) error  { return nil }
func (fakeDockerBackend) PullImage(context.Context, docker.PullOptions) (string, error) {
	return "", nil
}
func (fakeDockerBackend) RemoveImage(context.Context, string, bool) error { return nil }
func (fakeDockerBackend) OpenLogs(context.Context, docker.LogsOptions) (docker.LogStream, error) {
	return docker.LogStream{}, nil
}
func (fakeDockerBackend) OpenExec(context.Context, docker.ExecOptions) (docker.ExecSession, error) {
	return newFakeExecSession(), nil
}
func (fakeDockerBackend) ListDir(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (fakeDockerBackend) Close() error { return nil }

type fakeExecSession struct {
	done chan struct{}
	once sync.Once
}

func newFakeExecSession() *fakeExecSession {
	return &fakeExecSession{done: make(chan struct{})}
}

func (s *fakeExecSession) Read([]byte) (int, error) {
	<-s.done
	return 0, io.EOF
}
func (s *fakeExecSession) Write(data []byte) (int, error) { return len(data), nil }
func (s *fakeExecSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
func (s *fakeExecSession) CloseWrite() error                        { return nil }
func (s *fakeExecSession) Resize(context.Context, uint, uint) error { return nil }
func (s *fakeExecSession) IsTTY() bool                              { return true }
func (s *fakeExecSession) Wait(ctx context.Context) (int, error) {
	select {
	case <-s.done:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func TestProductionMissingTmuxKeepsDockerReattachFallback(t *testing.T) {
	factory := &bridgeTestFactory{}
	dockerService := docker.NewService(docker.StaticBackends{
		SDKBackend:     fakeDockerBackend{},
		CommandBackend: fakeDockerBackend{},
	})
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir: t.TempDir(), Desktop: true, DurableBinary: "nexterm-no-such-tmux-binary",
		SupervisorStateDir: blockedSupervisorStateDir(t),
		Docker:             dockerService,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(production.Services.DurableErr, durable.ErrUnavailable) {
		t.Fatalf("durable constructor error = %v", production.Services.DurableErr)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })

	connectedResponse := dispatchDurableTest(t, production, "session_connect_local", `null`, "docker-channel", "client-a")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)

	execResponse := dispatchDurableTest(t, production, "docker_exec_attach", `{"args":{"sessionId":"`+connected.ID+`","containerId":"container-1","cols":80,"rows":24}}`, "docker-channel", "client-a")
	var streamID string
	requireStoreTestResponse(t, execResponse, &streamID)

	detachResponse := dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+streamID+`","mode":"detach"}`, "docker-channel", "client-a")
	requireProductionNull(t, detachResponse)

	attachResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+streamID+`","replayBytes":1024}`, "docker-channel-2", "client-a")
	var attached attachedTabDTO
	requireStoreTestResponse(t, attachResponse, &attached)
	if attached.TabID != streamID {
		t.Fatalf("docker reattach masked by missing tmux: %+v", attached)
	}
}
