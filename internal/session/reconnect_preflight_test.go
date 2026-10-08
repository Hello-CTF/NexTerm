package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStartReconnectPreflight(t *testing.T) {
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })

	if err := manager.StartReconnect("missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing session = %v, want ErrSessionNotFound", err)
	}

	local, err := manager.Connect(context.Background(), Asset{ID: "preflight-local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartReconnect(local.ID); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("local session = %v, want ErrUnsupported", err)
	}

	remote, err := manager.Connect(context.Background(), Asset{ID: "preflight-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartReconnect(remote.ID); err != nil {
		t.Fatalf("ssh session preflight = %v, want nil", err)
	}
}
