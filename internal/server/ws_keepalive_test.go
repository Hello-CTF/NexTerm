package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/coder/websocket"
)

func wsTestConfig(t *testing.T, keepAlive, pingTimeout, writeTimeout time.Duration) Config {
	t.Helper()
	config := testConfig(t, false)
	config.WebSocket = WebSocketConfig{KeepAlive: keepAlive, PingTimeout: pingTimeout, WriteTimeout: writeTimeout}
	return config
}

func TestWebSocketKeepAliveDisconnectsUnresponsivePeer(t *testing.T) {
	server, httpServer := newTestHTTP(t, wsTestConfig(t, 50*time.Millisecond, 100*time.Millisecond, time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 1 })
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 0 })
	if _, _, err := connection.Read(ctx); err == nil {
		t.Fatal("unresponsive peer kept the connection open")
	}
}

func TestWebSocketKeepAliveResponsivePeerStaysConnected(t *testing.T) {
	server, httpServer := newTestHTTP(t, wsTestConfig(t, 50*time.Millisecond, 200*time.Millisecond, time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 1 })

	var mu sync.Mutex
	var events []string
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_, data, err := connection.Read(ctx)
			if err != nil {
				return
			}
			mu.Lock()
			events = append(events, string(data))
			mu.Unlock()
		}
	}()

	time.Sleep(400 * time.Millisecond)
	if server.Events().SubscriberCount() != 1 {
		t.Fatal("responsive peer was disconnected by keepalive")
	}
	if err := server.Events().Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]string{"sessionId": "s1"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) == 1
	})
	if err := connection.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	<-readDone
}

func TestWebSocketKeepAliveDisabledLeavesIdlePeerAlone(t *testing.T) {
	server, httpServer := newTestHTTP(t, wsTestConfig(t, -1, 100*time.Millisecond, time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 1 })
	time.Sleep(300 * time.Millisecond)
	if server.Events().SubscriberCount() != 1 {
		t.Fatal("idle peer was closed with keepalive disabled")
	}
}

func TestPumpSocketWriteTimeoutClosesStuckPeer(t *testing.T) {
	server, err := New(wsTestConfig(t, -1, 100*time.Millisecond, 100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 16<<20)
	returned := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		server.pumpSocket(context.Background(), connection, func(context.Context) (websocket.MessageType, []byte, error) {
			return websocket.MessageBinary, payload, nil
		})
		close(returned)
	}))
	defer httpServer.Close()
	connection, _, err := websocket.Dial(context.Background(), strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/channel/stuck", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("pumpSocket stayed blocked on a stuck writer")
	}
}

func TestParseWebSocketEnv(t *testing.T) {
	config, err := ParseWebSocketEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if config.KeepAlive != 0 || config.PingTimeout != 0 || config.WriteTimeout != 0 {
		t.Fatalf("empty env config = %+v", config)
	}
	env := map[string]string{
		"NEXTERM_WS_KEEPALIVE":     "5s",
		"NEXTERM_WS_PING_TIMEOUT":  "3s",
		"NEXTERM_WS_WRITE_TIMEOUT": "2s",
	}
	config, err = ParseWebSocketEnv(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if config.KeepAlive != 5*time.Second || config.PingTimeout != 3*time.Second || config.WriteTimeout != 2*time.Second {
		t.Fatalf("env config = %+v", config)
	}
	if _, err := ParseWebSocketEnv(func(string) string { return "abc" }); err == nil {
		t.Fatal("invalid duration was accepted")
	}
}

func TestWebSocketConfigWithDefaults(t *testing.T) {
	config := WebSocketConfig{}.withDefaults()
	if config.KeepAlive != DefaultWebSocketKeepAlive || config.PingTimeout != DefaultWebSocketPingTimeout || config.WriteTimeout != DefaultWebSocketWriteTimeout {
		t.Fatalf("default config = %+v", config)
	}
	config = WebSocketConfig{KeepAlive: -1, PingTimeout: -1, WriteTimeout: -1}.withDefaults()
	if config.KeepAlive != -1 {
		t.Fatalf("negative keepalive must disable pings, got %v", config.KeepAlive)
	}
	if config.PingTimeout != DefaultWebSocketPingTimeout || config.WriteTimeout != DefaultWebSocketWriteTimeout {
		t.Fatalf("non-positive timeouts must fall back to defaults, got %+v", config)
	}
}
