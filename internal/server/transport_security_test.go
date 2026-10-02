package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRPCContentTypeAndOriginAdmission(t *testing.T) {
	_, httpServer := newTestHTTP(t, testConfig(t, false))
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/rpc", strings.NewReader(`{"cmd":"sync_digest","args":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "text/plain")
	response, err := httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain RPC status = %d", response.StatusCode)
	}

	request, err = http.NewRequest(http.MethodPost, httpServer.URL+"/rpc", strings.NewReader(`{"cmd":"sync_digest","args":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example")
	response, err = httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("untrusted Origin status = %d", response.StatusCode)
	}

	request, err = http.NewRequest(http.MethodPost, httpServer.URL+"/rpc", strings.NewReader(`{"cmd":"sync_digest","args":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:1420")
	response, err = httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || body["ok"] != true || response.Header.Get("Access-Control-Allow-Origin") != "http://localhost:1420" {
		t.Fatalf("development Origin response = %d %+v %+v", response.StatusCode, body, response.Header)
	}

	request, err = http.NewRequest(http.MethodOptions, httpServer.URL+"/rpc", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "http://localhost:1420")
	request.Header.Set("Access-Control-Request-Method", "POST")
	request.Header.Set("Access-Control-Request-Headers", "content-type")
	response, err = httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || !strings.Contains(response.Header.Get("Access-Control-Allow-Headers"), "Content-Type") {
		t.Fatalf("preflight response = %d %+v", response.StatusCode, response.Header)
	}
}

func TestSyncOnlyPreflightDoesNotCreateExtraRoutes(t *testing.T) {
	_, httpServer := newTestHTTP(t, testConfig(t, true))
	for path, expected := range map[string]int{"/sync/rpc": http.StatusNoContent, "/rpc": http.StatusNotFound, "/ws/events": http.StatusNotFound} {
		request, err := http.NewRequest(http.MethodOptions, httpServer.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", "http://localhost:1420")
		request.Header.Set("Access-Control-Request-Method", "POST")
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != expected {
			t.Errorf("OPTIONS %s = %d, want %d", path, response.StatusCode, expected)
		}
	}
}

func TestWebSocketRejectsUntrustedOrigin(t *testing.T) {
	_, httpServer := newTestHTTP(t, testConfig(t, false))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/events", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err == nil {
		connection.Close(websocket.StatusNormalClosure, "")
		t.Fatal("untrusted Origin was accepted")
	}
	if response != nil {
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("untrusted websocket status = %d", response.StatusCode)
		}
	}

	connection, _, err = websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/events", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{httpServer.URL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	connection.Close(websocket.StatusNormalClosure, "")
}
