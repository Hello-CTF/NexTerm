package sync

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

type countingWriter struct {
	net.Conn
	total *atomic.Int64
}

func (w countingWriter) Write(p []byte) (int, error) {
	// Count before writing so the total never lags behind what the client has already received.
	w.total.Add(int64(len(p)))
	return w.Conn.Write(p)
}

// countingProxy forwards TCP to target and counts bytes flowing from the server back to the client.
func countingProxy(t *testing.T, target string) (addr string, fromServer *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	counter := &atomic.Int64{}
	var group stdsync.WaitGroup
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			group.Add(2)
			go func() {
				defer group.Done()
				_, _ = io.Copy(upstream, client)
				_ = upstream.Close()
			}()
			go func() {
				defer group.Done()
				_, _ = io.Copy(countingWriter{client, counter}, upstream)
				_ = client.Close()
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().String(), counter
}

func TestRemoteWebSocketDialsCompressTextButNotOpaqueTunnels(t *testing.T) {
	text := []byte(strings.Repeat("INFO 2026-10-24 worker processed request id=12345 status=200\r\n", 1000)[:60000])
	const frames = 10
	var offered atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offered.Store(r.Header.Get("Sec-WebSocket-Extensions"))
		// A server on the default policy accepts whatever the client offers.
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionNoContextTakeover})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for i := 0; i < frames; i++ {
			if err := conn.Write(r.Context(), websocket.MessageBinary, text); err != nil {
				return
			}
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer server.Close()

	run := func(purpose string) (wire int64, extensions string) {
		addr, counter := countingProxy(t, strings.TrimPrefix(server.URL, "http://"))
		client := &remoteClient{base: "http://" + addr, insecure: false}
		conn, err := client.dialDeviceBridge(context.Background(), "device-1", purpose)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		received, err := io.Copy(io.Discard, conn)
		if err != nil && received < int64(frames*len(text)) {
			t.Fatalf("read %d of %d bytes: %v", received, frames*len(text), err)
		}
		if received != int64(frames*len(text)) {
			t.Fatalf("received %d bytes, want %d", received, frames*len(text))
		}
		extensions, _ = offered.Load().(string)
		return counter.Load(), extensions
	}

	plain := int64(frames * len(text))
	wire, extensions := run("terminal")
	if !strings.Contains(extensions, "permessage-deflate") {
		t.Fatalf("terminal dial offered %q, want permessage-deflate", extensions)
	}
	if ratio := float64(wire) / float64(plain); ratio >= 0.5 {
		t.Fatalf("terminal wire/plain = %.2f, want < 0.5", ratio)
	}
	wire, extensions = run("forward")
	if extensions != "" {
		t.Fatalf("forward dial offered %q, want no compression", extensions)
	}
	if wire < plain {
		t.Fatalf("forward wire bytes %d below plain %d", wire, plain)
	}
}
