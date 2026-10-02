package session

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type dialerProvider interface {
	CurrentDialer(context.Context, string) (base.Dialer, error)
}

var _ dialerProvider = (*Manager)(nil)

type fakeDialerTransport struct {
	*fakeTransport
	dials atomic.Int32
}

func (t *fakeDialerTransport) DialContext(context.Context, string, string) (net.Conn, error) {
	t.dials.Add(1)
	return nil, errors.New("unexpected DialContext call")
}

func TestCurrentDialerResolvesReplacementWithoutDialing(t *testing.T) {
	var created []*fakeDialerTransport
	connector := ConnectorFunc(func(_ context.Context, asset Asset, generation uint64) (base.Transport, error) {
		transport := &fakeDialerTransport{fakeTransport: newFakeTransport(asset.Kind, generation+1000)}
		created = append(created, transport)
		return transport, nil
	})
	manager := NewManager(Config{Connector: connector, ReconnectBackoff: []time.Duration{0}})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "ssh-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	dialer, err := manager.CurrentDialer(context.Background(), session.ID)
	if err != nil || dialer != created[0] {
		t.Fatalf("initial dialer = %v, err = %v", dialer, err)
	}
	if err := manager.Disconnect(session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CurrentDialer(context.Background(), session.ID); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("disconnected dialer error = %v", err)
	}
	if err := manager.Reconnect(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := manager.CurrentDialer(context.Background(), session.ID)
	if err != nil || replacement != created[1] || replacement == created[0] {
		t.Fatalf("replacement dialer = %v, err = %v", replacement, err)
	}
	for index, transport := range created {
		if got := transport.dials.Load(); got != 0 {
			t.Fatalf("transport %d DialContext calls = %d, resolution must not dial", index, got)
		}
	}
}

func TestCurrentDialerRejectsUnknownUnsupportedAndCanceled(t *testing.T) {
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.CurrentDialer(context.Background(), "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("unknown session error = %v", err)
	}
	session, err := manager.Connect(context.Background(), Asset{ID: "local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CurrentDialer(context.Background(), session.ID); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("local dialer error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.CurrentDialer(ctx, session.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled resolver error = %v", err)
	}
}
