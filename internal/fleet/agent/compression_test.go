package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

func TestDialBridgeOffersCompressionByPurpose(t *testing.T) {
	var mu sync.Mutex
	offered := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		offered = r.Header.Get("Sec-WebSocket-Extensions")
		mu.Unlock()
		// An older server on context takeover must stay compatible with every purpose.
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err := conn.Read(r.Context()); err != nil {
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"hello_ok"}`))
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	for purpose, wantCompression := range map[string]bool{
		"": true, PurposeTerminal: true, "file": true,
		"forward": false, "listen": false, "accept": false,
	} {
		conn, err := DialBridge(context.Background(), wsURL, HelloMessage{Type: "hello", BridgeID: "b1", Purpose: purpose}, false)
		if err != nil {
			t.Fatalf("purpose %q: %v", purpose, err)
		}
		_ = conn.CloseNow()
		mu.Lock()
		got := strings.Contains(offered, "permessage-deflate")
		mu.Unlock()
		if got != wantCompression {
			t.Errorf("purpose %q offered compression = %v (%q), want %v", purpose, got, offered, wantCompression)
		}
	}
}
