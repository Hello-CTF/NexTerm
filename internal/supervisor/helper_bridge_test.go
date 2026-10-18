package supervisor

import (
	"context"
	"errors"
	"net"
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
