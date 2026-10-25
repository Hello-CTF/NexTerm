package wscompress

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	want := map[Purpose]Setting{
		Events:   {websocket.CompressionNoContextTakeover, 1024},
		Terminal: {websocket.CompressionContextTakeover, 128},
		File:     {websocket.CompressionContextTakeover, 128},
		Control:  {websocket.CompressionNoContextTakeover, 4096},
		Device:   {websocket.CompressionContextTakeover, 1024},
		Public:   {websocket.CompressionNoContextTakeover, 512},
	}
	for p, expected := range want {
		if got := apply(p, overrideNone); got != expected {
			t.Errorf("purpose %d default = %+v, want %+v", p, got, expected)
		}
		if got := apply(p, overrideTakeover); got.Mode != websocket.CompressionContextTakeover {
			t.Errorf("purpose %d takeover override = %+v", p, got)
		}
		if got := apply(p, overrideNoContext); got.Mode != websocket.CompressionNoContextTakeover {
			t.Errorf("purpose %d nocontext override = %+v", p, got)
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

func TestHandshakeKeepsContextForTerminalAndStaysConservativeForEvents(t *testing.T) {
	payload := bytes.Repeat([]byte("terminal output line\r\n"), 3000)
	extension, echoed := negotiate(t, Terminal, &websocket.DialOptions{CompressionMode: websocket.CompressionContextTakeover}, payload)
	if !strings.Contains(extension, "permessage-deflate") || strings.Contains(extension, "server_no_context_takeover") {
		t.Errorf("terminal with a takeover client: extension = %q, want context kept", extension)
	}
	if !bytes.Equal(echoed, payload) {
		t.Error("terminal payload corrupted")
	}
	// Peers that do not offer context takeover still connect, and event sockets never keep the context.
	for name, tc := range map[string]struct {
		purpose Purpose
		client  *websocket.DialOptions
	}{
		"terminal with a nocontext client": {Terminal, &websocket.DialOptions{CompressionMode: websocket.CompressionNoContextTakeover}},
		"events with a takeover client":    {Events, &websocket.DialOptions{CompressionMode: websocket.CompressionContextTakeover}},
	} {
		extension, echoed := negotiate(t, tc.purpose, tc.client, payload)
		if !strings.Contains(extension, "permessage-deflate") {
			t.Errorf("%s: extension = %q", name, extension)
		}
		if tc.purpose == Events && !strings.Contains(extension, "server_no_context_takeover") {
			t.Errorf("%s: events must not keep the compression context, extension = %q", name, extension)
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

// TestIdleConnectionsDoNotRetainFlateWriters guards the memory cost of context takeover: a connection that only ever
// sends tiny messages (the agent control channel, event sockets) must never allocate the ~0.8 MB flate writer. The
// bound is generous (an order of magnitude above the measured cost) so it stays deterministic.
func TestIdleConnectionsDoNotRetainFlateWriters(t *testing.T) {
	if envOverride() != overrideNone {
		t.Skip("compression policy overridden by environment")
	}
	const pairs = 20
	small := []byte(`{"event":"session://status","payload":{"status":"connected"}}`)
	release := make(chan struct{})
	var written sync.WaitGroup
	written.Add(pairs)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, Accept(Device, nil))
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for i := 0; i < 3; i++ {
			_ = conn.Write(r.Context(), websocket.MessageText, small)
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

type countingListener struct {
	net.Listener
	written *atomic.Int64
}

func (l countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return countingConn{Conn: conn, written: l.written}, nil
}

type countingConn struct {
	net.Conn
	written *atomic.Int64
}

func (c countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Add(int64(n))
	return n, err
}

// wireBytes sends the same stream of text frames over a server with the given purpose and returns the bytes the
// server wrote to the network.
func wireBytes(t *testing.T, purpose Purpose, frames [][]byte) int64 {
	t.Helper()
	var written atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, Accept(purpose, nil))
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for _, frame := range frames {
			if err := conn.Write(r.Context(), websocket.MessageBinary, frame); err != nil {
				return
			}
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	server.Listener = countingListener{Listener: server.Listener, written: &written}
	server.Start()
	defer server.Close()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	for range frames {
		if _, _, err := conn.Read(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return written.Load()
}

// Bandwidth guard: terminal output reaches the network in frames of a few KiB, where keeping the compression context
// across messages is what makes the link cheap. If the terminal policy ever drops the context again this fails.
func TestTerminalChannelsKeepTheContextToSaveBandwidth(t *testing.T) {
	if envOverride() != overrideNone {
		t.Skip("compression policy overridden by environment")
	}
	line := "2026-10-24T10:11:12Z INFO worker-%03d processed request id=%06d status=200 bytes=%d latency=%dms\r\n"
	var frames [][]byte
	n := 0
	for len(frames) < 200 {
		var frame []byte
		for len(frame) < 3000 {
			frame = append(frame, []byte(fmt.Sprintf(line, n%17, n, 1000+n%977, n%93))...)
			n++
		}
		frames = append(frames, frame)
	}
	var raw int64
	for _, frame := range frames {
		raw += int64(len(frame))
	}
	terminal := wireBytes(t, Terminal, frames)
	perMessage := wireBytes(t, Events, frames) // no-context-takeover policy: every message compressed alone
	t.Logf("raw=%d terminal=%d (%.1f%%) per-message=%d (%.1f%%)", raw, terminal, 100*float64(terminal)/float64(raw), perMessage, 100*float64(perMessage)/float64(raw))
	if terminal >= perMessage*85/100 {
		t.Fatalf("terminal channel wrote %d bytes, per-message compression %d: the context must save at least 15%%", terminal, perMessage)
	}
}
