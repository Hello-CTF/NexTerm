package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestLoopbackHostValidation(t *testing.T) {
	for host, expected := range map[string]bool{
		"localhost":          true,
		"localhost:8080":     true,
		"LOCALHOST:8080":     true,
		"localhost.":         true,
		"127.0.0.1":          true,
		"127.0.0.1:8080":     true,
		"[::1]":              true,
		"[::1]:8080":         true,
		"::1":                true,
		"evil.com":           false,
		"evil.com:8080":      false,
		"localhost.evil.com": false,
		"127.0.0.2:8080":     false,
		"192.168.1.10:8080":  false,
		"user@localhost":     false,
		"":                   false,
	} {
		if got := loopbackHost(host); got != expected {
			t.Errorf("loopbackHost(%q) = %v, want %v", host, got, expected)
		}
	}
}

func TestLoopbackModeRejectsNonLoopbackHost(t *testing.T) {
	config := testConfig(t, false)
	config.Options.Auth = AuthLoopback
	_, httpServer := newTestHTTP(t, config)

	post := func(host string) int {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/rpc", strings.NewReader(`{"cmd":"sync_digest","args":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Host = host
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}

	for host, expected := range map[string]int{
		"evil.com":       http.StatusMisdirectedRequest,
		"evil.com:8080":  http.StatusMisdirectedRequest,
		"127.0.0.2:8080": http.StatusMisdirectedRequest,
		"localhost:8080": http.StatusOK,
		"127.0.0.1":      http.StatusOK,
		"[::1]":          http.StatusOK,
	} {
		if status := post(host); status != expected {
			t.Errorf("Host %q = %d, want %d", host, status, expected)
		}
	}
}

func TestHostValidationAppliesOnlyToLoopbackModeOnLoopbackListen(t *testing.T) {
	exposed := exposedConfig(t)
	exposed.Options.Auth = AuthLoopback
	_, httpServer := newTestHTTP(t, exposed)

	post := func(host string, token bool) int {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/rpc", strings.NewReader(`{"cmd":"sync_digest","args":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Host = host
		if token {
			request.Header.Set(TokenHeader, "secret")
		}
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}

	if status := post("evil.com", false); status != http.StatusUnauthorized {
		t.Fatalf("non-loopback listen tokenless = %d, want %d", status, http.StatusUnauthorized)
	}
	if status := post("evil.com", true); status != http.StatusOK {
		t.Fatalf("non-loopback listen with token = %d, want %d", status, http.StatusOK)
	}

	config := testConfig(t, false)
	config.Options.Auth = AuthOn
	_, httpServer = newTestHTTP(t, config)
	if status := post("evil.com", false); status != http.StatusUnauthorized {
		t.Fatalf("auth=on tokenless with foreign Host = %d, want %d", status, http.StatusUnauthorized)
	}
	if status := post("evil.com", true); status != http.StatusOK {
		t.Fatalf("auth=on with token and foreign Host = %d, want %d", status, http.StatusOK)
	}
}

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

func TestTerminalHandshakeFailuresAreSingleShot(t *testing.T) {
	for _, test := range []struct {
		name     string
		syncOnly bool
		method   string
		path     string
		origin   string
		expected int
	}{
		{name: "unauthorized", syncOnly: true, method: http.MethodPost, path: "/sync/rpc", expected: http.StatusUnauthorized},
		{name: "forbidden", method: http.MethodGet, path: "/ws/events", origin: "https://evil.example", expected: http.StatusForbidden},
		{name: "not found", syncOnly: true, method: http.MethodGet, path: "/ws/events", expected: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, err := New(testConfig(t, test.syncOnly))
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				server.Handler().ServeHTTP(w, r)
			}))
			t.Cleanup(func() {
				httpServer.Close()
				_ = server.Close()
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if test.method == http.MethodPost {
				request, err := http.NewRequestWithContext(ctx, test.method, httpServer.URL+test.path, strings.NewReader(`{"cmd":"sync_digest","args":{}}`))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				response, err := httpServer.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != test.expected {
					t.Fatalf("status = %d, want %d", response.StatusCode, test.expected)
				}
			} else {
				options := &websocket.DialOptions{}
				if test.origin != "" {
					options.HTTPHeader = http.Header{"Origin": []string{test.origin}}
				}
				connection, response, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+test.path, options)
				if err == nil {
					connection.Close(websocket.StatusNormalClosure, "")
					t.Fatalf("handshake unexpectedly succeeded")
				}
				if response != nil {
					response.Body.Close()
					if response.StatusCode != test.expected {
						t.Fatalf("status = %d, want %d", response.StatusCode, test.expected)
					}
				}
			}
			if count := requests.Load(); count != 1 {
				t.Fatalf("request count = %d, want exactly one terminal attempt", count)
			}
		})
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
