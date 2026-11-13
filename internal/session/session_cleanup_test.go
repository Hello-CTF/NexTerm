package session

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// offlineAfterFirstConnect connects once and fails every later dial.
func offlineAfterFirstConnect() *fakeConnector {
	connector := newFakeConnector()
	connector.connect = func(_ context.Context, asset Asset, _ uint64, call int) (base.Transport, error) {
		if call > 1 {
			return nil, ipc.NewError(ipc.CodeDisconnected, "offline")
		}
		return newFakeTransport(asset.Kind, uint64(call)+1000), nil
	}
	return connector
}

// Disconnecting a session whose connection is already gone and that holds no
// terminal removes it.
func TestDisconnectRemovesADeadSessionWithoutTerminals(t *testing.T) {
	connector := offlineAfterFirstConnect()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		ReconnectBackoff: []time.Duration{10 * time.Millisecond}, ReconnectMax: 2, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "dead-host", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartReconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, manager, connected.ID, StatusFailed)
	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Session(connected.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("session lookup after removing a dead session = %v, want not found", err)
	}
}

// A dead session that still carries a terminal a client watches is only
// marked disconnected; the terminal keeps its record.
func TestDisconnectKeepsADeadSessionWithAWatchedTerminal(t *testing.T) {
	connector := offlineAfterFirstConnect()
	manager, _, connected, info := openReproDurableTab(t, io.EOF, connector, 2)
	if err := manager.StartReconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, manager, connected.ID, StatusFailed)
	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Session(connected.ID); err != nil {
		t.Fatalf("a session with a watched terminal was removed: %v", err)
	}
	if _, err := manager.Tab(info.ID); err != nil {
		t.Fatalf("the watched terminal lost its record: %v", err)
	}
}
