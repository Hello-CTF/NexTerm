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
	tokens := &fakeTokenStore{token: "secret"}
	credentialVault := &fakeVault{}
	config.Tokens = tokens
	config.Vault = credentialVault
	config.Options.MasterKey = "correct-password"
	lifecycle := newTestLifecycle()
	ctx, cancel := context.WithCancel(context.Background())
	var stderr bytes.Buffer
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- Serve(ctx, ServeConfig{
			Server: config, Lifecycle: lifecycle, Tokens: tokens,
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
	status, body := postRPC(t, client, "http://"+listener.Addr().String()+"/sync/rpc", "sync_digest", map[string]string{TokenHeader: "secret"})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("sync response = %d %+v", status, body)
	}

	cancel()
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("graceful shutdown = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
	lifecycle.mu.Lock()
	started, shutdown := lifecycle.started, lifecycle.shutdown
	lifecycle.mu.Unlock()
	if started != 1 || shutdown != 1 {
		t.Fatalf("lifecycle start=%d shutdown=%d", started, shutdown)
	}
	tokens.mu.Lock()
	syncCalls := tokens.syncCalls
	tokens.mu.Unlock()
	if syncCalls == 0 {
		t.Fatal("startup did not ensure sync token")
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
