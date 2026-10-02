package forward

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

var _ DialerProvider = (*session.Manager)(nil)

type sessionDialTransport struct {
	*recordingDialer
	kind       string
	generation uint64
	alive      atomic.Bool
}

func newSessionDialTransport(upstream string, generation uint64) *sessionDialTransport {
	transport := &sessionDialTransport{
		recordingDialer: &recordingDialer{upstream: upstream},
		kind:            session.KindSSH,
		generation:      generation,
	}
	transport.alive.Store(true)
	return transport
}

func (t *sessionDialTransport) Kind() string {
	return t.kind
}

func (t *sessionDialTransport) Generation() uint64 {
	return t.generation
}

func (t *sessionDialTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}

func (t *sessionDialTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}

func (t *sessionDialTransport) IsAlive() bool {
	return t.alive.Load()
}

func (t *sessionDialTransport) Close() error {
	t.alive.Store(false)
	return nil
}

func (t *sessionDialTransport) OpenPTY(context.Context, base.PTYOptions) (base.Channel, error) {
	return nil, errors.New("unexpected PTY open without tabs")
}

func TestSessionManagerReconnectReplacementAndBoundedRetry(t *testing.T) {
	firstTransport := newSessionDialTransport(startEchoServer(t), 1)
	secondTransport := newSessionDialTransport(startEchoServer(t), 2)
	reconnectStarted := make(chan struct{})
	releaseReplacement := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseReplacement) })
	}
	var connectorMu sync.Mutex
	connectorCalls := 0
	connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		connectorMu.Lock()
		connectorCalls++
		call := connectorCalls
		connectorMu.Unlock()
		switch call {
		case 1:
			return firstTransport, nil
		case 2:
			close(reconnectStarted)
			<-releaseReplacement
			return secondTransport, nil
		default:
			return nil, errors.New("unexpected connector call")
		}
	})
	manager := session.NewManager(session.Config{
		Connector:        connector,
		NewID:            func() string { return "forward-session" },
		ReconnectMax:     1,
		ReconnectBackoff: []time.Duration{time.Millisecond},
		IdleTimeout:      -1,
	})
	t.Cleanup(func() { _ = manager.Close() })
	t.Cleanup(release)
	connectedSession, err := manager.Connect(t.Context(), session.Asset{ID: "ssh-asset", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(Config{
		Provider:         manager,
		Policy:           Policy{Desktop: true},
		Reconnect:        ReconnectPolicy{Attempts: 3, Backoff: 20 * time.Millisecond},
		DialTimeout:      time.Second,
		HandshakeTimeout: time.Second,
	})
	t.Cleanup(func() { _ = service.Close() })
	spec, err := service.CreateLocal(t.Context(), CreateLocalArgs{
		SessionID:  connectedSession.ID,
		TargetHost: "remote.internal",
		TargetPort: 22,
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := dialForward(t, spec)
	assertEcho(t, initial, "initial-transport")
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	reconnectResult := make(chan error, 1)
	go func() {
		reconnectResult <- manager.Reconnect(context.Background(), connectedSession.ID)
	}()
	select {
	case <-reconnectStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("session reconnect did not reach replacement connector")
	}

	duringReconnect := dialForward(t, spec)
	startedAt := time.Now()
	if err := duringReconnect.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := duringReconnect.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection remained open after bounded retry exhaustion")
	}
	if elapsed := time.Since(startedAt); elapsed < 30*time.Millisecond {
		t.Fatalf("bounded retry returned after %v; reconnect was not retried", elapsed)
	}
	if got := len(firstTransport.calls()); got != 1 {
		t.Fatalf("stale transport dial calls = %d, want only the initial connection", got)
	}
	if got := len(secondTransport.calls()); got != 0 {
		t.Fatalf("replacement used before reconnect commit: %d calls", got)
	}

	release()
	select {
	case err := <-reconnectResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session reconnect did not commit")
	}
	replacement := dialForward(t, spec)
	assertEcho(t, replacement, "replacement-transport")
	if got := len(firstTransport.calls()); got != 1 {
		t.Fatalf("stale transport used after reconnect: %d calls", got)
	}
	calls := secondTransport.calls()
	if len(calls) != 1 || calls[0] != "remote.internal:22" {
		t.Fatalf("replacement dial calls = %v", calls)
	}
}
