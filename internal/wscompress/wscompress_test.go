package wscompress

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

func TestParseOverride(t *testing.T) {
	cases := map[string]override{"": overrideNone, "bogus": overrideNone, "takeover": overrideTakeover, " NoContext ": overrideNoContext, "off": overrideOff}
	for input, want := range cases {
		if got := parseOverride(input); got != want {
			t.Errorf("parseOverride(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestPolicyByPurpose(t *testing.T) {
	for _, p := range []Purpose{Events, Terminal, File, Control, Device} {
		if got := apply(p, overrideNone); got.Mode != websocket.CompressionNoContextTakeover || got.Threshold < 512 {
			t.Errorf("purpose %d default = %+v", p, got)
		}
		if got := apply(p, overrideTakeover); got.Mode != websocket.CompressionContextTakeover {
			t.Errorf("purpose %d takeover override = %+v", p, got)
		}
		if got := apply(p, overrideOff); got.Mode != websocket.CompressionDisabled {
			t.Errorf("purpose %d off override = %+v", p, got)
		}
	}
	for _, o := range []override{overrideNone, overrideTakeover, overrideNoContext, overrideOff} {
		if got := apply(Opaque, o); got.Mode != websocket.CompressionDisabled {
			t.Errorf("opaque must never compress, got %+v", got)
		}
	}
	for purpose, want := range map[string]Purpose{"": Terminal, "terminal": Terminal, "file": File, "forward": Opaque, "listen": Opaque, "accept": Opaque} {
		if got := ForBridge(purpose); got != want {
			t.Errorf("ForBridge(%q) = %v, want %v", purpose, got, want)
		}
	}
}

// negotiate runs one handshake and returns the extension header the server answered with plus the echoed message.
func negotiate(t *testing.T, serverPurpose Purpose, client *websocket.DialOptions, payload []byte) (string, []byte) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, Accept(serverPurpose, nil))
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(1 << 20)
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageBinary, data)
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer server.Close()
	conn, response, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), client)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	if err := conn.Write(context.Background(), websocket.MessageBinary, payload); err != nil {
		t.Fatal(err)
	}
	_, echoed, err := conn.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return response.Header.Get("Sec-WebSocket-Extensions"), echoed
}

func TestHandshakeNegotiatesNoContextTakeoverEvenForTakeoverClients(t *testing.T) {
	payload := bytes.Repeat([]byte("terminal output line\r\n"), 3000)
	for name, client := range map[string]*websocket.DialOptions{
		"old takeover client": {CompressionMode: websocket.CompressionContextTakeover},
		"nocontext client":    {CompressionMode: websocket.CompressionNoContextTakeover},
	} {
		extension, echoed := negotiate(t, Terminal, client, payload)
		if !strings.Contains(extension, "permessage-deflate") || !strings.Contains(extension, "server_no_context_takeover") {
			t.Errorf("%s: extension = %q", name, extension)
		}
		if !bytes.Equal(echoed, payload) {
			t.Errorf("%s: payload corrupted", name)
		}
	}
}

func TestHandshakeOpaqueAndDisabledClientsStayUncompressed(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4096)
	extension, echoed := negotiate(t, Opaque, &websocket.DialOptions{CompressionMode: websocket.CompressionContextTakeover}, payload)
	if extension != "" || !bytes.Equal(echoed, payload) {
		t.Errorf("opaque server negotiated %q", extension)
	}
	extension, echoed = negotiate(t, Terminal, &websocket.DialOptions{CompressionMode: websocket.CompressionDisabled}, payload)
	if extension != "" || !bytes.Equal(echoed, payload) {
		t.Errorf("client without compression negotiated %q", extension)
	}
	extension, _ = negotiate(t, Terminal, Dial(Opaque, nil), payload)
	if extension != "" {
		t.Errorf("opaque dial negotiated %q", extension)
	}
}

func heapAlloc() uint64 {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

// TestIdleConnectionsDoNotRetainFlateWriters guards the memory regression of context takeover: with it every
// connection that once sent a frame above the threshold keeps a ~0.8 MB flate writer for its whole life. The bound is
// generous (an order of magnitude above the measured cost) so it stays deterministic.
func TestIdleConnectionsDoNotRetainFlateWriters(t *testing.T) {
	if envOverride() != overrideNone {
		t.Skip("compression policy overridden by environment")
	}
	const pairs = 20
	big := []byte(strings.Repeat("INFO 2026-10-24 worker processed request id=12345 status=200\r\n", 1000)[:60000])
	small := []byte(`{"event":"session://status","payload":{"status":"connected"}}`)
	release := make(chan struct{})
	var written sync.WaitGroup
	written.Add(pairs)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, Accept(Terminal, nil))
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for i := 0; i < 3; i++ {
			_ = conn.Write(r.Context(), websocket.MessageBinary, big)
		}
		_ = conn.Write(r.Context(), websocket.MessageText, small)
		written.Done()
		<-release
	}))
	defer server.Close()
	before := heapAlloc()
	clients := make([]*websocket.Conn, 0, pairs)
	for i := 0; i < pairs; i++ {
		conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{CompressionMode: websocket.CompressionContextTakeover})
		if err != nil {
			t.Fatal(err)
		}
		conn.SetReadLimit(1 << 20)
		clients = append(clients, conn)
	}
	for _, conn := range clients {
		for i := 0; i < 4; i++ {
			if _, _, err := conn.Read(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}
	written.Wait()
	after := heapAlloc()
	perPair := (int64(after) - int64(before)) / pairs
	t.Logf("heap per idle+active connection pair: %d KB", perPair/1024)
	if perPair > 100*1024 {
		t.Fatalf("heap per connection pair = %d KB, want < 100 KB", perPair/1024)
	}
	close(release)
	for _, conn := range clients {
		_ = conn.CloseNow()
	}
}
