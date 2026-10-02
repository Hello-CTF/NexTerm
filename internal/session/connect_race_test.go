package session

import (
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func TestDisconnectDuringInitialConnectRejectsLateTransport(t *testing.T) {
	connector := newFakeConnector()
	started := make(chan struct{})
	release := make(chan struct{})
	connector.connect = func(_ context.Context, asset Asset, _ uint64, call int) (base.Transport, error) {
		if call == 1 {
			close(started)
			<-release
		}
		return newFakeTransport(asset.Kind, uint64(call)+1000), nil
	}
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	asset := Asset{ID: "asset", Kind: KindSSH}
	connected := make(chan error, 1)
	go func() {
		_, err := manager.Connect(context.Background(), asset)
		connected <- err
	}()
	<-started
	infos := manager.ListSessions()
	if len(infos) != 1 || infos[0].Status != StatusConnecting {
		t.Fatalf("connecting registry = %+v", infos)
	}
	if err := manager.Disconnect(infos[0].ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-connected; !errors.Is(err, ErrDisconnected) {
		t.Fatalf("initial connect error = %v, want ErrDisconnected", err)
	}
	if got := connector.transport(0).closeCount.Load(); got != 1 {
		t.Fatalf("late initial transport close count = %d", got)
	}
	if got := infos[0].ID; got == "" {
		t.Fatal("empty disconnected session id")
	}
	replacement, err := manager.Connect(context.Background(), asset)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == infos[0].ID || replacement.Info().Status != StatusConnected {
		t.Fatalf("replacement reused disconnected session: %+v", replacement.Info())
	}
	if got := len(manager.ListSessions()); got != 2 {
		t.Fatalf("registry should retain disconnected history and one live session, got %d entries", got)
	}
}

func TestConnectErrorClosesReturnedTransport(t *testing.T) {
	connector := newFakeConnector()
	connector.connect = func(_ context.Context, asset Asset, _ uint64, call int) (base.Transport, error) {
		return newFakeTransport(asset.Kind, uint64(call)+1000), errors.New("connect failed")
	}
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH}); err == nil {
		t.Fatal("connect unexpectedly succeeded")
	}
	if got := connector.transport(0).closeCount.Load(); got != 1 {
		t.Fatalf("failed connect transport close count = %d", got)
	}
	if got := len(manager.ListSessions()); got != 0 {
		t.Fatalf("failed initial connect retained %d sessions", got)
	}
}
