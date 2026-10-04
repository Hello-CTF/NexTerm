package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/coder/websocket"
)

func exposedConfig(t *testing.T) Config {
	t.Helper()
	config := testConfig(t, false)
	config.Options.Listen = "0.0.0.0:8080"
	return config
}

func TestExposedListenRequiresTokenForRPC(t *testing.T) {
	_, httpServer := newTestHTTP(t, exposedConfig(t))
	url := httpServer.URL + "/rpc"

	status, body := postRPC(t, httpServer.Client(), url, "sync_digest", nil)
	if status != http.StatusUnauthorized || body.OK || body.Error == nil || body.Error.Code != ipc.CodeForbidden {
		t.Fatalf("missing token = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), url, "sync_digest", map[string]string{TokenHeader: "wrong"})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("wrong token = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), url, "sync_digest", map[string]string{TokenHeader: "secret"})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("valid token = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), url, "sync_digest", map[string]string{PlatformUserHeader: "forged"})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("forged platform identity = %d %+v", status, body)
	}
}

func TestExposedListenRejectsAfterTokenRotation(t *testing.T) {
	current := "before-rotation"
	config := exposedConfig(t)
	config.Tokens = TokenVerifierFunc(func(_ context.Context, token string) (bool, error) {
		return token == current, nil
	})
	_, httpServer := newTestHTTP(t, config)

	status, _ := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "sync_digest", map[string]string{TokenHeader: "before-rotation"})
	if status != http.StatusOK {
		t.Fatalf("token before rotation = %d", status)
	}
	current = "after-rotation"
	status, _ = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "sync_digest", map[string]string{TokenHeader: "before-rotation"})
	if status != http.StatusUnauthorized {
		t.Fatalf("rotated-out token = %d", status)
	}
	status, _ = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "sync_digest", map[string]string{TokenHeader: "after-rotation"})
	if status != http.StatusOK {
		t.Fatalf("rotated token = %d", status)
	}
}

func TestExposedListenVerifierErrorIsServerError(t *testing.T) {
	config := exposedConfig(t)
	config.Tokens = TokenVerifierFunc(func(context.Context, string) (bool, error) {
		return false, errors.New("store unavailable")
	})
	_, httpServer := newTestHTTP(t, config)

	status, _ := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "sync_digest", map[string]string{TokenHeader: "secret"})
	if status != http.StatusInternalServerError {
		t.Fatalf("verifier error status = %d", status)
	}
}

func TestExposedListenPlatformGatewayAdmission(t *testing.T) {
	config := exposedConfig(t)
	config.TrustPlatformUser = true
	_, httpServer := newTestHTTP(t, config)

	status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "sync_digest", map[string]string{PlatformUserHeader: "gateway-user"})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("platform gateway admission = %d %+v", status, body)
	}
}

func TestExposedListenKeepsHealthAndStaticPublic(t *testing.T) {
	config := exposedConfig(t)
	config.Static = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("index"))
	})
	_, httpServer := newTestHTTP(t, config)

	response, err := httpServer.Client().Get(httpServer.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", response.StatusCode)
	}
	response, err = httpServer.Client().Get(httpServer.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("static status = %d", response.StatusCode)
	}
}

func TestExposedListenRequiresTokenForBlobs(t *testing.T) {
	config := exposedConfig(t)
	config.Blobs = NewBlobStore(t.TempDir(), testLogger())
	_, httpServer := newTestHTTP(t, config)

	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/files/blob?name=a.txt", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("blob stage without token = %d", response.StatusCode)
	}

	request, err = http.NewRequest(http.MethodPost, httpServer.URL+"/files/blob?name=a.txt", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(TokenHeader, "secret")
	response, err = httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var staged struct {
		OK   bool `json:"ok"`
		Data struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&staged); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !staged.OK || staged.Data.ID == "" {
		t.Fatalf("blob stage with token = %d %+v", response.StatusCode, staged)
	}

	response, err = httpServer.Client().Get(httpServer.URL + "/files/blob?id=" + staged.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("blob download without token = %d", response.StatusCode)
	}
	request, err = http.NewRequest(http.MethodGet, httpServer.URL+"/files/blob?id="+staged.Data.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(TokenHeader, "secret")
	response, err = httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("blob download with token = %d", response.StatusCode)
	}
}

func TestExposedListenWebSocketAdmission(t *testing.T) {
	server, httpServer := newTestHTTP(t, exposedConfig(t))
	wsURL := strings.Replace(httpServer.URL, "http", "ws", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connection, response, err := websocket.Dial(ctx, wsURL+"/ws/events", nil)
	if err == nil {
		connection.Close(websocket.StatusNormalClosure, "")
		t.Fatal("websocket without token was accepted")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("websocket without token response = %+v", response)
	}
	if response != nil {
		response.Body.Close()
	}

	connection, response, err = websocket.Dial(ctx, wsURL+"/ws/events", &websocket.DialOptions{
		HTTPHeader: http.Header{TokenHeader: []string{"wrong"}},
	})
	if err == nil {
		connection.Close(websocket.StatusNormalClosure, "")
		t.Fatal("websocket with wrong token was accepted")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("websocket wrong token response = %+v", response)
	}
	if response != nil {
		response.Body.Close()
	}

	connection, _, err = websocket.Dial(ctx, wsURL+"/ws/events", &websocket.DialOptions{
		HTTPHeader: http.Header{TokenHeader: []string{"secret"}},
	})
	if err != nil {
		t.Fatalf("websocket with header token: %v", err)
	}
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 1 })
	_ = connection.Close(websocket.StatusNormalClosure, "")

	connection, response, err = websocket.Dial(ctx, wsURL+"/ws/events", &websocket.DialOptions{
		Subprotocols: []string{wsAuthProtocol, "wrong"},
	})
	if err == nil {
		connection.Close(websocket.StatusNormalClosure, "")
		t.Fatal("websocket with wrong subprotocol token was accepted")
	}
	if response != nil {
		response.Body.Close()
	}

	connection, response, err = websocket.Dial(ctx, wsURL+"/ws/events", &websocket.DialOptions{
		Subprotocols: []string{wsAuthProtocol, "secret"},
	})
	if err != nil {
		t.Fatalf("websocket with subprotocol token: %v", err)
	}
	if got := response.Header.Get("Sec-WebSocket-Protocol"); got != wsAuthProtocol {
		t.Fatalf("negotiated subprotocol = %q", got)
	}
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 1 })
	_ = connection.Close(websocket.StatusNormalClosure, "")

	connection, _, err = websocket.Dial(ctx, wsURL+"/ws/channel/any", &websocket.DialOptions{
		Subprotocols: []string{wsAuthProtocol, "secret"},
	})
	if err != nil {
		t.Fatalf("channel websocket with subprotocol token: %v", err)
	}
	_ = connection.Close(websocket.StatusNormalClosure, "")
}

func TestExposedListenWebSocketVerifierErrorIsServerError(t *testing.T) {
	config := exposedConfig(t)
	config.Tokens = TokenVerifierFunc(func(context.Context, string) (bool, error) {
		return false, errors.New("store unavailable")
	})
	_, httpServer := newTestHTTP(t, config)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/events", &websocket.DialOptions{
		HTTPHeader: http.Header{TokenHeader: []string{"secret"}},
	})
	if err == nil {
		connection.Close(websocket.StatusNormalClosure, "")
		t.Fatal("websocket handshake unexpectedly succeeded")
	}
	if response == nil || response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("websocket verifier error response = %+v", response)
	}
	if response != nil {
		response.Body.Close()
	}
}

func TestExposedListenSyncRPCHandlerCarriesVerifier(t *testing.T) {
	config := exposedConfig(t)
	config.Tokens = nil
	config.SyncRPC = &authCarryingHandler{token: "secret", platformTrusted: true}
	_, httpServer := newTestHTTP(t, config)

	status, _ := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "sync_digest", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("missing token = %d", status)
	}
	status, _ = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "sync_digest", map[string]string{TokenHeader: "secret"})
	if status != http.StatusOK {
		t.Fatalf("carried verifier token = %d", status)
	}
	status, _ = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "sync_digest", map[string]string{PlatformUserHeader: "gateway-user"})
	if status != http.StatusOK {
		t.Fatalf("carried platform trust = %d", status)
	}
}

type authCarryingHandler struct {
	token           string
	platformTrusted bool
}

func (h *authCarryingHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (h *authCarryingHandler) VerifyToken(_ context.Context, token string) (bool, error) {
	return token == h.token, nil
}

func (h *authCarryingHandler) PlatformTrusted() bool { return h.platformTrusted }

func TestExposedListenWithoutVerifierFailsClosed(t *testing.T) {
	config := exposedConfig(t)
	config.Tokens = nil
	if _, err := New(config); err == nil {
		t.Fatal("non-loopback listen without token verifier was accepted")
	}

	loopback := testConfig(t, false)
	loopback.Tokens = nil
	loopback.SyncRPC = nil
	if _, err := New(loopback); err == nil {
		t.Fatal("missing sync RPC handler and token verifier was accepted")
	}
}
