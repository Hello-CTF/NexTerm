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

	core "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/coder/websocket"
)

type testLifecycle struct {
	mu       sync.Mutex
	started  int
	shutdown int
	ready    chan struct{}
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
	application, err := core.New(core.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var lifecycle testLifecycle
	lifecycle.ready = make(chan struct{})
	if err := application.AddComponent("test", core.ComponentFuncs{
		StartFunc: func(context.Context) error {
			lifecycle.mu.Lock()
			lifecycle.started++
			lifecycle.mu.Unlock()
			close(lifecycle.ready)
			return nil
		},
		ShutdownFunc: func(context.Context) error {
			lifecycle.mu.Lock()
			lifecycle.shutdown++
			lifecycle.mu.Unlock()
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := BootstrapVault(context.Background(), credentialVault, config.Options.MasterKey); err != nil {
		t.Fatal(err)
	}
	httpServer, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer httpServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	var stderr bytes.Buffer
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- application.Serve(ctx, core.ServeConfig{
			Listen: "127.0.0.1:0", Listener: listener, Transport: httpServer.Handler(),
			CloseTransport: httpServer.CloseContext, Stderr: &stderr,
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

// Cancelling the serve context (SIGTERM) must deliver the going-away close to open WebSockets instead of dropping them.
func TestServeCancelSendsGoingAway(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	application, err := core.New(core.Config{})
	if err != nil {
		t.Fatal(err)
	}
	httpServer, err := New(testConfig(t, false))
	if err != nil {
		t.Fatal(err)
	}
	defer httpServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- application.Serve(ctx, core.ServeConfig{
			Listen: "127.0.0.1:0", Listener: listener, Transport: httpServer.Handler(),
			CloseTransport: httpServer.CloseContext, Stderr: &bytes.Buffer{},
		})
	}()
	dialCtx, stopDial := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopDial()
	connection, _, err := websocket.Dial(dialCtx, "ws://"+listener.Addr().String()+"/ws/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	waitFor(t, func() bool { return httpServer.Events().SubscriberCount() == 1 })
	cancel()
	_, _, err = connection.Read(dialCtx)
	if status := websocket.CloseStatus(err); status != websocket.StatusGoingAway {
		t.Fatalf("close status = %v (%v), want going away", status, err)
	}
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("shutdown = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after cancellation")
	}
}
