package production

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent/purpose"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
)

// fakeDeviceAgentDial serves forward bridges with the real agent-side dial server, like the agent does.
func fakeDeviceAgentDial(t *testing.T) device.DialFunc {
	t.Helper()
	serveCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	server := purpose.NewDialServer(nil)
	return func(_ context.Context, bridgePurpose string) (net.Conn, error) {
		if bridgePurpose != deviceproto.PurposeForward {
			return nil, errors.New("unexpected purpose " + bridgePurpose)
		}
		client, serverSide := net.Pipe()
		go func() { _ = server.ServeConn(serveCtx, serverSide) }()
		return client, nil
	}
}

func TestDeviceDockerSDKBackendReachesEngineSocketThroughForwardBridge(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "d.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			_, _ = w.Write([]byte(`[{"Id":"abc123","Names":["/web"],"Image":"nginx","State":"running"}]`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	httpServer := &http.Server{Handler: mux}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })

	transport := device.New("dev-1", "", fakeDeviceAgentDial(t), 1, "linux")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	backend, err := newDeviceDockerSDKBackend(ctx, transport, socket)
	if err != nil {
		t.Fatal(err)
	}
	containers, err := backend.ListContainers(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 || containers[0].ID != "abc123" {
		t.Fatalf("containers = %+v; want the fake daemon's container", containers)
	}
}

func TestDeviceDockerSDKBackendFallsBackWhenSocketUnreachable(t *testing.T) {
	transport := device.New("dev-1", "", fakeDeviceAgentDial(t), 1, "linux")
	_, err := newDeviceDockerSDKBackend(t.Context(), transport, filepath.Join(t.TempDir(), "missing.sock"))
	if !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("err = %v; want ErrUnsupported so the pool falls back to the CLI backend", err)
	}
}

func TestDeviceDockerSDKBackendWindowsUsesCommandBackend(t *testing.T) {
	transport := device.New("dev-1", "", fakeDeviceAgentDial(t), 1, "windows")
	_, err := newDeviceDockerSDKBackend(t.Context(), transport, "/var/run/docker.sock")
	if !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("err = %v; want ErrUnsupported for windows", err)
	}
}

func TestDeviceDockerCommandOpenerExplainsMissingStreams(t *testing.T) {
	transport := device.New("dev-1", "", fakeDeviceAgentDial(t), 1, "linux")
	_, err := dockerCommandOpener{transport: transport}.OpenCommand(t.Context(), "docker logs x", 80, 24)
	if !errors.Is(err, docker.ErrUnsupported) {
		t.Fatalf("err = %v; want docker.ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "升级") {
		t.Fatalf("err = %q; want an actionable Chinese hint", err)
	}
}

func TestDeviceDockerStreamsUnavailableNamesProbeCause(t *testing.T) {
	cause := errors.New("permission denied")
	err := dockerCommandOpener{cause: cause}.cause
	message := deviceDockerStreamsUnavailable{cause: err}.Error()
	if !strings.Contains(message, "permission denied") {
		t.Fatalf("message = %q; want the probe failure cause", message)
	}
}
