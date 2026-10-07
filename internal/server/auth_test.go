package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
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

func TestAuthModeMatrixAcrossListens(t *testing.T) {
	for _, mode := range []string{AuthOn, AuthLoopback, AuthOff} {
		for _, listen := range []string{"127.0.0.1:0", "0.0.0.0:8080"} {
			name := mode + "/" + listen
			t.Run(name, func(t *testing.T) {
				config := testConfig(t, false)
				config.Options.Listen = listen
				config.Options.Auth = mode
				_, httpServer := newTestHTTP(t, config)

				authRequired := mode == AuthOn || mode == AuthLoopback && listen != "127.0.0.1:0"

				status, _ := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", nil)
				if authRequired && status != http.StatusUnauthorized {
					t.Fatalf("tokenless RPC = %d, want %d", status, http.StatusUnauthorized)
				}
				if !authRequired && status != http.StatusOK {
					t.Fatalf("tokenless RPC = %d, want %d", status, http.StatusOK)
				}
				status, _ = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", map[string]string{TokenHeader: "secret"})
				if status != http.StatusOK {
					t.Fatalf("token RPC = %d, want %d", status, http.StatusOK)
				}

				request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/files/blob?name=matrix.txt", strings.NewReader("payload"))
				if err != nil {
					t.Fatal(err)
				}
				response, err := httpServer.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if authRequired && response.StatusCode != http.StatusUnauthorized {
					t.Fatalf("tokenless blob = %d, want %d", response.StatusCode, http.StatusUnauthorized)
				}
				if !authRequired && response.StatusCode != http.StatusOK {
					t.Fatalf("tokenless blob = %d, want %d", response.StatusCode, http.StatusOK)
				}

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				connection, wsResponse, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/events", nil)
				if authRequired {
					if err == nil {
						connection.Close(websocket.StatusNormalClosure, "")
						t.Fatal("tokenless websocket was accepted")
					}
					if wsResponse == nil || wsResponse.StatusCode != http.StatusUnauthorized {
						t.Fatalf("tokenless websocket response = %+v", wsResponse)
					}
					if wsResponse != nil {
						wsResponse.Body.Close()
					}
				} else {
					if err != nil {
						t.Fatalf("tokenless websocket: %v", err)
					}
					_ = connection.Close(websocket.StatusNormalClosure, "")
				}
			})
		}
	}
}

func TestAuthModeConfigurationValidation(t *testing.T) {
	config := testConfig(t, false)
	config.Options.Auth = "bogus"
	if _, err := New(config); err == nil {
		t.Fatal("invalid auth mode was accepted")
	}

	plainHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	requireVerifier := func(mode, listen string) error {
		t.Helper()
		config := testConfig(t, false)
		config.Options.Listen = listen
		config.Options.Auth = mode
		config.Tokens = nil
		config.SyncObjects = plainHandler
		_, err := New(config)
		return err
	}
	if err := requireVerifier(AuthOn, "127.0.0.1:0"); err == nil {
		t.Fatal("auth=on without token verifier was accepted")
	}
	if err := requireVerifier(AuthLoopback, "0.0.0.0:8080"); err == nil {
		t.Fatal("auth=loopback on non-loopback listen without token verifier was accepted")
	}
	if err := requireVerifier(AuthLoopback, "127.0.0.1:0"); err != nil {
		t.Fatalf("auth=loopback on loopback listen: %v", err)
	}
	if err := requireVerifier(AuthOff, "0.0.0.0:8080"); err != nil {
		t.Fatalf("auth=off: %v", err)
	}
}

func TestExposedListenRequiresTokenForRPC(t *testing.T) {
	_, httpServer := newTestHTTP(t, exposedConfig(t))
	url := httpServer.URL + "/rpc"

	status, body := postRPC(t, httpServer.Client(), url, "app_info", nil)
	if status != http.StatusUnauthorized || body.OK || body.Error == nil || body.Error.Code != ipc.CodeForbidden {
		t.Fatalf("missing token = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), url, "app_info", map[string]string{TokenHeader: "wrong"})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("wrong token = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), url, "app_info", map[string]string{TokenHeader: "secret"})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("valid token = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), url, "app_info", map[string]string{"X-HC-User-ID": "forged"})
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

	status, _ := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", map[string]string{TokenHeader: "before-rotation"})
	if status != http.StatusOK {
		t.Fatalf("token before rotation = %d", status)
	}
	current = "after-rotation"
	status, _ = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", map[string]string{TokenHeader: "before-rotation"})
	if status != http.StatusUnauthorized {
		t.Fatalf("rotated-out token = %d", status)
	}
	status, _ = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", map[string]string{TokenHeader: "after-rotation"})
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

	status, _ := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", map[string]string{TokenHeader: "secret"})
	if status != http.StatusInternalServerError {
		t.Fatalf("verifier error status = %d", status)
	}
}

func TestExposedListenGatewayKeyAdmission(t *testing.T) {
	config := exposedConfig(t)
	config.GatewayAuthKey = "gateway-secret"
	_, httpServer := newTestHTTP(t, config)

	status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", map[string]string{GatewayAuthHeader: "gateway-secret"})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("gateway key admission = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", map[string]string{GatewayAuthHeader: "wrong"})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("wrong gateway key = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", map[string]string{"X-HC-User-ID": "forged"})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("forged platform identity header = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", map[string]string{"X-HC-User-ID": "forged", GatewayAuthHeader: "wrong"})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("forged platform header with wrong gateway key = %d %+v", status, body)
	}

	connection, response, err := websocket.Dial(context.Background(), strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/events", &websocket.DialOptions{
		HTTPHeader: http.Header{GatewayAuthHeader: []string{"gateway-secret"}},
	})
	if err != nil {
		t.Fatalf("websocket gateway key admission: %v", response)
	}
	_ = connection.Close(websocket.StatusNormalClosure, "")
}

func TestExposedListenEmptyGatewayKeyIgnoresHeader(t *testing.T) {
	_, httpServer := newTestHTTP(t, exposedConfig(t))
	status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", map[string]string{GatewayAuthHeader: "anything"})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("gateway header without configured key = %d %+v", status, body)
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

func TestExposedListenWebSocketTokenVerificationIsBounded(t *testing.T) {
	var verifyCalls atomic.Int32
	config := exposedConfig(t)
	config.Tokens = TokenVerifierFunc(func(_ context.Context, token string) (bool, error) {
		verifyCalls.Add(1)
		return token == "secret", nil
	})
	_, httpServer := newTestHTTP(t, config)
	wsURL := strings.Replace(httpServer.URL, "http", "ws", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dial := func(options *websocket.DialOptions) error {
		connection, response, err := websocket.Dial(ctx, wsURL+"/ws/events", options)
		if err != nil {
			if response != nil && response.Body != nil {
				response.Body.Close()
			}
			return err
		}
		_ = connection.Close(websocket.StatusNormalClosure, "")
		return nil
	}

	garbage := make([]string, 2000)
	for i := range garbage {
		garbage[i] = "junk"
	}
	if err := dial(&websocket.DialOptions{Subprotocols: garbage}); err == nil {
		t.Fatal("garbage candidates were accepted")
	}
	if calls := verifyCalls.Load(); calls != 0 {
		t.Fatalf("garbage candidates triggered %d verifications", calls)
	}

	if err := dial(&websocket.DialOptions{Subprotocols: []string{wsAuthProtocol, "secret", "extra"}}); err == nil {
		t.Fatal("extra candidates were accepted")
	}
	if calls := verifyCalls.Load(); calls != 0 {
		t.Fatalf("extra candidates triggered %d verifications", calls)
	}

	if err := dial(&websocket.DialOptions{Subprotocols: []string{wsAuthProtocol}}); err == nil {
		t.Fatal("marker without token was accepted")
	}
	if calls := verifyCalls.Load(); calls != 0 {
		t.Fatalf("missing token triggered %d verifications", calls)
	}

	if err := dial(&websocket.DialOptions{Subprotocols: []string{wsAuthProtocol, "wrong"}}); err == nil {
		t.Fatal("wrong token was accepted")
	}
	if calls := verifyCalls.Load(); calls != 1 {
		t.Fatalf("wrong token verifications = %d", calls)
	}

	if err := dial(&websocket.DialOptions{Subprotocols: []string{wsAuthProtocol, "secret"}}); err != nil {
		t.Fatalf("valid subprotocol token: %v", err)
	}
	if calls := verifyCalls.Load(); calls != 2 {
		t.Fatalf("valid token verifications = %d", calls)
	}

	if err := dial(&websocket.DialOptions{
		HTTPHeader:   http.Header{TokenHeader: []string{"wrong"}},
		Subprotocols: []string{wsAuthProtocol, "secret"},
	}); err == nil {
		t.Fatal("wrong header token with valid subprotocol was accepted")
	}
	if calls := verifyCalls.Load(); calls != 3 {
		t.Fatalf("header token must not fall through to subprotocol, verifications = %d", calls)
	}
}

func TestExposedListenWebSocketGatewayKeySkipsTokenVerification(t *testing.T) {
	var verifyCalls atomic.Int32
	config := exposedConfig(t)
	config.GatewayAuthKey = "gateway-secret"
	config.Tokens = TokenVerifierFunc(func(_ context.Context, token string) (bool, error) {
		verifyCalls.Add(1)
		return token == "secret", nil
	})
	_, httpServer := newTestHTTP(t, config)
	wsURL := strings.Replace(httpServer.URL, "http", "ws", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	garbage := make([]string, 2000)
	for i := range garbage {
		garbage[i] = "junk"
	}
	connection, _, err := websocket.Dial(ctx, wsURL+"/ws/events", &websocket.DialOptions{
		HTTPHeader:   http.Header{GatewayAuthHeader: []string{"gateway-secret"}},
		Subprotocols: garbage,
	})
	if err != nil {
		t.Fatalf("gateway key with garbage candidates: %v", err)
	}
	_ = connection.Close(websocket.StatusNormalClosure, "")
	if calls := verifyCalls.Load(); calls != 0 {
		t.Fatalf("gateway key triggered %d verifications", calls)
	}
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

func TestExposedListenWithoutVerifierFailsClosed(t *testing.T) {
	config := exposedConfig(t)
	config.Tokens = nil
	if _, err := New(config); err == nil {
		t.Fatal("non-loopback listen without token verifier was accepted")
	}

	loopback := testConfig(t, false)
	loopback.Tokens = nil
	loopback.SyncObjects = nil
	if _, err := New(loopback); err == nil {
		t.Fatal("missing sync object handler and token verifier was accepted")
	}
}

// M165: 账号会话经 /rpc 下发时, 处理器同时拿到 server 私有 identity 与 ipc 共享 userID;
// 无会话/CSRF 缺失的原鉴权行为不变。
func TestRequireAuthAccountSessionInjectsSharedUserID(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	err := fixture.server.dispatcher.RegisterRaw("test_echo_user", func(ctx context.Context, _ *ipc.Call) (any, error) {
		userID, ok := ipc.UserIDFromContext(ctx)
		result := map[string]any{"userID": userID, "ok": ok}
		if identity := accountIdentityFrom(ctx); identity != nil {
			result["identityUserID"] = identity.UserID
		}
		return result, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := fixture.initSuperadmin(t, "admin", "correct-horse-battery")
	userID, _ := session.user["id"].(string)
	if userID == "" {
		t.Fatalf("session user missing id: %v", session.user)
	}

	echo := func(session *accountTestSession, csrf string) accountCall {
		return fixture.call(t, http.MethodPost, "/rpc", map[string]any{"cmd": "test_echo_user", "args": map[string]any{}}, session, csrf, nil)
	}

	authorized := echo(session, session.csrf)
	if authorized.status != http.StatusOK {
		t.Fatalf("authorized rpc status=%d body=%v", authorized.status, authorized.body)
	}
	data, _ := authorized.body["data"].(map[string]any)
	if data["ok"] != true || data["userID"] != userID || data["identityUserID"] != userID {
		t.Fatalf("injected identity mismatch: %v", data)
	}

	anonymous := echo(nil, "")
	if anonymous.status != http.StatusUnauthorized {
		t.Fatalf("anonymous rpc status=%d, want 401", anonymous.status)
	}
	noCSRF := echo(session, "")
	if noCSRF.status != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d, want 403", noCSRF.status)
	}
}
