package session

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestReconnectOnceForDialsOnceAndReturnsActualError(t *testing.T) {
	connector := newFakeConnector()
	manager := newReconnectOnceManager(t, connector)
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
	manager := newReconnectOnceManager(t, connector)
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
	manager := newReconnectOnceManager(t, connector)
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
	manager := newReconnectOnceManager(t, connector)
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
