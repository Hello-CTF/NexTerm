//go:build unix

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// v2HelperServer answers every hello exactly as protocol 2 helpers did: a
// version_mismatch error that names neither side.
func v2HelperServer(t *testing.T) string {
	t.Helper()
	runDir, err := os.MkdirTemp("/tmp", "nx-sup-v2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	path := filepath.Join(runDir, "supervisor.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				if _, _, err := readFrame(conn); err != nil {
					return
				}
				_ = writeFrame(conn, frameError, []byte(`{"code":"version_mismatch","message":"protocol version 3 is not supported"}`))
			}()
		}
	}()
	return path
}

func assertHelperUpgrade(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrHelperUpgradeRequired) {
		t.Fatalf("error = %v, want ErrHelperUpgradeRequired", err)
	}
	if errors.Is(err, base.ErrDisconnected) {
		t.Fatalf("error = %v, must not read as a dropped connection", err)
	}
	if !strings.Contains(err.Error(), "needs upgrade") {
		t.Fatalf("error = %q, want a readable upgrade reason", err)
	}
}

func assertLegacyHelper(t *testing.T, err error) {
	t.Helper()
	assertHelperUpgrade(t, err)
	if !errors.Is(err, ErrLegacyHelperRunning) {
		t.Fatalf("error = %v, want ErrLegacyHelperRunning", err)
	}
}

func TestV3ClientReportsHelperUpgradeForV2Helper(t *testing.T) {
	path := v2HelperServer(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	client := NewClient(path, stateDir)
	_, err := client.List(context.Background())
	assertHelperUpgrade(t, err)
	if errors.Is(err, ErrLegacyHelperRunning) {
		t.Fatalf("v2 helper answering itself = %v, must not read as a leftover legacy helper", err)
	}

	remote := NewRemoteClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}, "digest")
	_, err = NewRemoteProvider(remote).ListDurable(context.Background())
	if !errors.Is(err, durable.ErrUnavailable) {
		t.Fatalf("provider error = %v, want durable.ErrUnavailable", err)
	}
	assertHelperUpgrade(t, err)
}

func TestHelperTellsOlderClientToUpdate(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	conn := dialServer(t, server.SocketPath())
	payload, err := marshalFrame(frameHello, helloMsg{Version: ProtocolVersion - 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameHello, payload); err != nil {
		t.Fatal(err)
	}
	kind, response := readErrorFrame(t, conn)
	if kind != frameError {
		t.Fatalf("response frame = %d, want error", kind)
	}
	var msg errorMsg
	if err := unmarshalFrame(response, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Code != codeVersionMismatch || msg.Version != ProtocolVersion || !strings.Contains(msg.Message, "update the client") {
		t.Fatalf("rejection = %+v, want a version mismatch telling the client to update", msg)
	}
}

// legacyHelperEndpoint occupies the endpoint an older helper for stateDir
// would serve, without touching the state directory itself.
func legacyHelperEndpoint(t *testing.T, stateDir string) {
	t.Helper()
	endpoint, err := helperEndpointVersion(stateDir, ProtocolVersion-1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(endpoint)) })
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
}

func TestLegacyHelperOnStateDirReportsUpgrade(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyHelperEndpoint(t, stateDir)
	endpoint, err := HelperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("local client", func(t *testing.T) {
		_, err := NewClient(endpoint, stateDir).List(ctx)
		assertLegacyHelper(t, err)
	})
	t.Run("bridge", func(t *testing.T) {
		digest, err := stateDigestFor(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		client := NewRemoteClient(func(ctx context.Context) (net.Conn, error) {
			return DialHelperBridge(ctx, stateDir)
		}, digest)
		_, err = client.List(ctx)
		assertLegacyHelper(t, err)
	})
	t.Run("connect does not spawn", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "no-such-helper")
		_, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, Executable: missing, SpawnTimeout: time.Second})
		assertLegacyHelper(t, err)
	})
}

func TestMissingHelperIsUnavailableNotUpgrade(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	endpoint, err := HelperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewClient(endpoint, stateDir).List(context.Background())
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("List without any helper = %v, want ErrUnavailable only", err)
	}
	if _, err := DialHelperBridge(context.Background(), stateDir); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("DialHelperBridge without any helper = %v, want ErrUnavailable", err)
	}
}
