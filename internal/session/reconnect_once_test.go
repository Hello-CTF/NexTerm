package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

func newReconnectOnceManager(t *testing.T, connector *fakeConnector) *Manager {
	t.Helper()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		ReconnectMax: 10, ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

// newOpenReconnectOnceManager is the desktop/open shape: identity-less
// callers are admitted through the open manager.
func newOpenReconnectOnceManager(t *testing.T, connector *fakeConnector) *Manager {
	t.Helper()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		ReconnectMax: 10, ReconnectBackoff: []time.Duration{0}, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func TestReconnectOnceForDialsOnceAndReturnsActualError(t *testing.T) {
	connector := newFakeConnector()
	manager := newOpenReconnectOnceManager(t, connector)
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	initialCalls := connector.callCount()
	connectErr := &ssh.ConnectError{Kind: ssh.ErrorKindNetwork, Err: errors.New("temporary failure")}
	connector.mu.Lock()
	connector.connect = func(context.Context, Asset, uint64, int) (base.Transport, error) {
		return nil, connectErr
	}
	connector.mu.Unlock()

	if err := manager.ReconnectOnceFor(context.Background(), connected.ID, Asset{ID: "asset", Kind: KindSSH}); !errors.Is(err, connectErr) {
		t.Fatalf("reconnect error = %v, want the actual dial error %v", err, connectErr)
	}
	if got := connector.callCount(); got != initialCalls+1 {
		t.Fatalf("connector calls = %d, want exactly one dial (%d)", got, initialCalls+1)
	}
	// The retryable failure must not schedule any background retry.
	time.Sleep(50 * time.Millisecond)
	if got := connector.callCount(); got != initialCalls+1 {
		t.Fatalf("background dial after return: %d calls", got)
	}
	info := connected.Info()
	if info.Status != StatusFailed || info.Attempt != 1 || info.NextRetryAt != nil {
		t.Fatalf("failed session info = %+v, want one attempt without another retry", info)
	}
}

func TestReconnectOnceForStopsOnCallerCancel(t *testing.T) {
	connector := newFakeConnector()
	manager := newOpenReconnectOnceManager(t, connector)
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	initialCalls := connector.callCount()
	dialStarted := make(chan struct{})
	connector.mu.Lock()
	connector.connect = func(ctx context.Context, _ Asset, _ uint64, _ int) (base.Transport, error) {
		close(dialStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	connector.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.ReconnectOnceFor(ctx, connected.ID, Asset{ID: "asset", Kind: KindSSH})
	}()
	<-dialStarted
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled reconnect = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled reconnect did not return")
	}
	time.Sleep(50 * time.Millisecond)
	if got := connector.callCount(); got != initialCalls+1 {
		t.Fatalf("background dial after cancel: %d calls", got)
	}
}

func TestReconnectOnceForCallerDeadline(t *testing.T) {
	connector := newFakeConnector()
	manager := newOpenReconnectOnceManager(t, connector)
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	initialCalls := connector.callCount()
	connector.mu.Lock()
	connector.connect = func(ctx context.Context, _ Asset, _ uint64, _ int) (base.Transport, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	connector.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := manager.ReconnectOnceFor(ctx, connected.ID, Asset{ID: "asset", Kind: KindSSH}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline reconnect = %v, want context.DeadlineExceeded", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := connector.callCount(); got != initialCalls+1 {
		t.Fatalf("background dial after deadline: %d calls", got)
	}
}

func TestReconnectOnceForSuccess(t *testing.T) {
	connector := newFakeConnector()
	manager := newOpenReconnectOnceManager(t, connector)
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	initialCalls := connector.callCount()
	if err := manager.ReconnectOnceFor(context.Background(), connected.ID, Asset{ID: "asset", Kind: KindSSH}); err != nil {
		t.Fatalf("reconnect = %v", err)
	}
	if got := connector.callCount(); got != initialCalls+1 {
		t.Fatalf("connector calls = %d, want exactly one dial (%d)", got, initialCalls+1)
	}
	if connected.Info().Status != StatusConnected {
		t.Fatalf("session status = %q, want connected", connected.Info().Status)
	}
}

func TestReconnectOnceForNonOpenManagerRequiresOwner(t *testing.T) {
	connector := newFakeConnector()
	manager := newReconnectOnceManager(t, connector)
	ownerA := ipc.WithUserID(context.Background(), "owner-a")
	connected, err := manager.Connect(ownerA, Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	initialCalls := connector.callCount()
	initialTransport := connected.transport
	if initialTransport == nil {
		t.Fatal("session has no initial transport")
	}
	initialFake, ok := initialTransport.Transport.(*fakeTransport)
	if !ok {
		t.Fatalf("initial transport = %T, want *fakeTransport", initialTransport.Transport)
	}

	// Identity-less callers are rejected before any preflight, snapshot
	// write, or dial on a non-open manager.
	if err := manager.ReconnectOnceFor(context.Background(), connected.ID, Asset{ID: "asset", Kind: KindSSH}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("identity-less reconnect = %v, want ErrForbidden", err)
	}
	// Another owner is rejected the same way; neither call may touch the
	// session, its asset snapshot, or its live transport.
	ownerB := ipc.WithUserID(context.Background(), "owner-b")
	if err := manager.ReconnectOnceFor(ownerB, connected.ID, Asset{ID: "asset", Kind: KindSSH}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-owner reconnect = %v, want ErrForbidden", err)
	}
	if got := connector.callCount(); got != initialCalls {
		t.Fatalf("forbidden reconnect dialed: calls = %d, want %d", got, initialCalls)
	}
	if snapshot := connected.assetSnapshot.Load(); snapshot != nil {
		t.Fatalf("forbidden reconnect stored an asset snapshot: %+v", snapshot)
	}
	if connected.transport != initialTransport {
		t.Fatal("forbidden reconnect swapped the transport")
	}
	if closes := initialFake.closeCount.Load(); closes != 0 || !initialFake.IsAlive() {
		t.Fatalf("forbidden reconnect closed the original transport: closes=%d alive=%v", closes, initialFake.IsAlive())
	}
	if connected.staleConfig {
		t.Fatal("forbidden reconnect cleared the stale config")
	}

	// The legitimate owner keeps the single waitable reconnect.
	if err := manager.ReconnectOnceFor(ownerA, connected.ID, Asset{ID: "asset", Kind: KindSSH}); err != nil {
		t.Fatalf("owner reconnect = %v", err)
	}
	if got := connector.callCount(); got != initialCalls+1 {
		t.Fatalf("owner reconnect dials = %d, want exactly one (%d)", got, initialCalls+1)
	}
	if connected.Info().Status != StatusConnected {
		t.Fatalf("session status = %q, want connected", connected.Info().Status)
	}
}
