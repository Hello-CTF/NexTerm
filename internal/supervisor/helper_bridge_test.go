package supervisor

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDialHelperBridgeReconnectsToRestartedEndpoint(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	instance, err := New(Config{StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = instance.Close() }()
	digest, err := stateDigestFor(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := HelperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	client := NewRemoteClient(func(ctx context.Context) (net.Conn, error) {
		return DialHelperBridge(ctx, stateDir)
	}, digest)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for range 2 {
		server, err := NewServer(instance, endpoint)
		if err != nil {
			t.Fatal(err)
		}
		infos, listErr := client.List(ctx)
		shutdownErr := server.Shutdown(ctx)
		if listErr != nil || len(infos) != 0 {
			t.Fatalf("helper bridge list = %v, %v", infos, listErr)
		}
		if shutdownErr != nil {
			t.Fatal(shutdownErr)
		}
	}
	if conn, err := DialHelperBridge(ctx, stateDir); !errors.Is(err, ErrUnavailable) {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatalf("dial stopped helper = %v, want ErrUnavailable", err)
	}
}

// A bridge ends when the helper drops its connection even though sshd never
// closes its stdin, which is how a helper releases the bridge of a client
// whose SSH connection died.
func TestBridgeEndsWhenHelperDropsConnection(t *testing.T) {
	stdin, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdinWriter.Close() }()
	helperSide, bridgeSide := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- bridgeIO(stdin, io.Discard, bridgeSide) }()
	_ = helperSide.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge kept running after the helper dropped its connection")
	}
}
