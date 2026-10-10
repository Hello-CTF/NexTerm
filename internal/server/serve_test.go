package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type testLifecycle struct {
	mu       sync.Mutex
	started  int
	shutdown int
	ready    chan struct{}
	stopped  chan struct{}
	startErr error
}

func newTestLifecycle() *testLifecycle {
	return &testLifecycle{ready: make(chan struct{}), stopped: make(chan struct{})}
}

func (l *testLifecycle) Start(context.Context) error {
	if l.startErr != nil {
		return l.startErr
	}
	l.mu.Lock()
	l.started++
	l.mu.Unlock()
	close(l.ready)
	return nil
}

func (l *testLifecycle) Shutdown(context.Context) error {
	l.mu.Lock()
	l.shutdown++
	l.mu.Unlock()
	close(l.stopped)
	return nil
}

func TestServeBootstrapRealHTTPAndGracefulShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig(t, false)
	hub := newReplayHub()
	config.Channels = hub
	config.ChannelStats = hub.Stats
	credentialVault := &fakeVault{}
	config.Vault = credentialVault
	config.Options.MasterKey = "correct-password"
	lifecycle := newTestLifecycle()
	ctx, cancel := context.WithCancel(context.Background())
	var stderr bytes.Buffer
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- Serve(ctx, ServeConfig{
			Server: config, Lifecycle: lifecycle,
			Stderr: &stderr, Listener: listener, ShutdownTimeout: 2 * time.Second,
		})
	}()
	select {
	case <-lifecycle.ready:
	case err := <-serveResult:
		t.Fatalf("serve exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var health Health
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !health.OK || health.Vault == nil {
		t.Fatalf("health = %+v", health)
	}
	status, body := postRPC(t, client, "http://"+listener.Addr().String()+"/rpc", "app_info", nil)
	if status != http.StatusOK || !body.OK {
		t.Fatalf("rpc response = %d %+v", status, body)
	}
	dialCtx, stopDial := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopDial()
	connection, _, err := websocket.Dial(dialCtx, "ws://"+listener.Addr().String()+"/ws/channel/shutdown", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	waitFor(t, func() bool { return hub.Stats().LiveChannels == 1 })

	cancel()
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("graceful shutdown = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
	if hub.Stats().LiveChannels != 0 {
		t.Fatal("websocket channel remained attached after shutdown")
	}
	lifecycle.mu.Lock()
	started, shutdown := lifecycle.started, lifecycle.shutdown
	lifecycle.mu.Unlock()
	if started != 1 || shutdown != 1 {
		t.Fatalf("lifecycle start=%d shutdown=%d", started, shutdown)
	}
	credentialVault.mu.Lock()
	initialized := credentialVault.initialized
	credentialVault.mu.Unlock()
	if initialized != 1 {
		t.Fatalf("vault initialized %d times", initialized)
	}
	if stderr.Len() != 0 {
		t.Fatalf("loopback warning = %q", stderr.String())
	}
}
