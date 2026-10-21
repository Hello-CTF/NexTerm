//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
)

func TestProductionTerminalAttachExplicitlyDisablesSSHDurability(t *testing.T) {
	factory := &bridgeTestFactory{}
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir:   t.TempDir(),
		Desktop:   true,
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
		Kind: "ssh", Name: "volatile-ssh", Host: &host, Port: &port, Username: &username,
	})
	if err != nil {
		t.Fatal(err)
	}
	connectedResponse := dispatchDurableTest(t, production, "session_connect", `{"args":{"assetId":"`+assetRow.ID+`"}}`, "ssh-channel", "client-a")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)

	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24,"durable":false}`, "ssh-channel", "client-a")
	tabID := requireAttachedTabID(t, attachResponse)
	if tabID == "" {
		t.Fatal("ssh terminal_attach returned no tab")
	}

	localResponse := dispatchDurableTest(t, production, "session_connect_local", `null`, "local-channel", "client-a")
	var local sessionInfoDTO
	requireStoreTestResponse(t, localResponse, &local)
	localAttach := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+local.ID+`","cols":80,"rows":24}`, "local-channel", "client-a")
	localTabID := requireAttachedTabID(t, localAttach)
	if localTabID == "" {
		t.Fatal("local attach returned no tab")
	}
	if streams := factory.streams["local-channel"]; len(streams) != 1 {
		t.Fatalf("local attach did not bridge the channel: %+v", streams)
	}
	tabs := production.Services.Sessions.ListTabs()
	if len(tabs) != 2 {
		t.Fatalf("tabs = %+v", tabs)
	}
	for _, tab := range tabs {
		if tab.Durable {
			t.Fatalf("tab %s is durable, want volatile", tab.ID)
		}
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
