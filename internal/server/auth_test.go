package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
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
				config.Accounts = newTestAccounts(t)
				_, httpServer := newTestHTTP(t, config)

				authRequired := mode == AuthOn || mode == AuthLoopback && listen != "127.0.0.1:0"

				status, _ := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", nil)
				if authRequired && status != http.StatusUnauthorized {
					t.Fatalf("anonymous RPC = %d, want %d", status, http.StatusUnauthorized)
				}
				if !authRequired && status != http.StatusOK {
					t.Fatalf("anonymous RPC = %d, want %d", status, http.StatusOK)
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
					t.Fatalf("anonymous blob = %d, want %d", response.StatusCode, http.StatusUnauthorized)
				}
				if !authRequired && response.StatusCode != http.StatusOK {
					t.Fatalf("anonymous blob = %d, want %d", response.StatusCode, http.StatusOK)
				}

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				connection, wsResponse, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/events", nil)
				if authRequired {
					if err == nil {
						connection.Close(websocket.StatusNormalClosure, "")
						t.Fatal("anonymous websocket was accepted")
					}
					if wsResponse == nil || wsResponse.StatusCode != http.StatusUnauthorized {
						t.Fatalf("anonymous websocket response = %+v", wsResponse)
					}
					if wsResponse != nil {
						wsResponse.Body.Close()
					}
				} else {
					if err != nil {
						t.Fatalf("anonymous websocket: %v", err)
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
	requireAccounts := func(mode, listen string) error {
		t.Helper()
		config := testConfig(t, false)
		config.Options.Listen = listen
		config.Options.Auth = mode
		config.SyncObjects = plainHandler
		_, err := New(config)
		return err
	}
	if err := requireAccounts(AuthOn, "127.0.0.1:0"); err == nil {
		t.Fatal("auth=on without account service was accepted")
	}
	if err := requireAccounts(AuthLoopback, "0.0.0.0:8080"); err == nil {
		t.Fatal("auth=loopback on non-loopback listen without account service was accepted")
	}
	if err := requireAccounts(AuthLoopback, "127.0.0.1:0"); err != nil {
		t.Fatalf("auth=loopback on loopback listen: %v", err)
	}
	if err := requireAccounts(AuthOff, "0.0.0.0:8080"); err != nil {
		t.Fatalf("auth=off: %v", err)
	}
}

func TestExposedListenRequiresSessionForRPC(t *testing.T) {
	fixture := newAccountFixtureListen(t, AuthOn, "0.0.0.0:8080")
	url := fixture.http.URL + "/rpc"

	status, body := postRPC(t, fixture.client, url, "app_info", nil)
	if status != http.StatusUnauthorized || body.OK || body.Error == nil || body.Error.Code != ipc.CodeForbidden {
		t.Fatalf("missing credential = %d %+v", status, body)
	}
	status, body = postRPC(t, fixture.client, url, "app_info", map[string]string{"X-HC-User-ID": "forged"})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("forged platform identity = %d %+v", status, body)
	}

	session, _ := fixture.initSuperadmin(t, "root", "password-rpc-1")
	call := fixture.call(t, http.MethodPost, "/rpc", map[string]any{"cmd": "app_info", "args": map[string]any{}}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("session rpc = %d body=%v", call.status, call.body)
	}
}

func TestExposedListenGatewayKeyAdmission(t *testing.T) {
	config := exposedConfig(t)
	config.GatewayAuthKey = "gateway-secret"
	config.Accounts = newTestAccounts(t)
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
	config := exposedConfig(t)
	config.Accounts = newTestAccounts(t)
	_, httpServer := newTestHTTP(t, config)
	status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", map[string]string{GatewayAuthHeader: "anything"})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("gateway header without configured key = %d %+v", status, body)
	}
}

func TestExposedListenKeepsHealthAndStaticPublic(t *testing.T) {
	config := exposedConfig(t)
	config.Accounts = newTestAccounts(t)
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

func TestExposedListenRequiresSessionForBlobs(t *testing.T) {
	fixture := newAccountFixtureListen(t, AuthOn, "0.0.0.0:8080")
	session, _ := fixture.initSuperadmin(t, "root", "password-blob-1")

	stage := func(withSession bool) (int, string) {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, fixture.http.URL+"/files/blob?name=a.txt", strings.NewReader("payload"))
		if err != nil {
			t.Fatal(err)
		}
		if withSession {
			request.AddCookie(session.cookie)
			request.Header.Set(csrfHeaderName, session.csrf)
		}
		response, err := fixture.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var staged struct {
			OK   bool `json:"ok"`
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if response.StatusCode == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&staged); err != nil {
				t.Fatal(err)
			}
		}
		return response.StatusCode, staged.Data.ID
	}

	if status, _ := stage(false); status != http.StatusUnauthorized {
		t.Fatalf("blob stage without credential = %d", status)
	}
	status, id := stage(true)
	if status != http.StatusOK || id == "" {
		t.Fatalf("blob stage with session = %d id=%q", status, id)
	}

	download := func(withSession bool) int {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, fixture.http.URL+"/files/blob?id="+id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if withSession {
			request.AddCookie(session.cookie)
		}
		response, err := fixture.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if status := download(false); status != http.StatusUnauthorized {
		t.Fatalf("blob download without credential = %d", status)
	}
	if status := download(true); status != http.StatusOK {
		t.Fatalf("blob download with session = %d", status)
	}
}

func TestExposedListenWebSocketAdmission(t *testing.T) {
	fixture := newAccountFixtureListen(t, AuthOn, "0.0.0.0:8080")
	session, _ := fixture.initSuperadmin(t, "root", "password-ws-1")
	wsURL := strings.Replace(fixture.http.URL, "http", "ws", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connection, response, err := websocket.Dial(ctx, wsURL+"/ws/events", nil)
	if err == nil {
		connection.Close(websocket.StatusNormalClosure, "")
		t.Fatal("websocket without credential was accepted")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("websocket without credential response = %+v", response)
	}
	if response != nil {
		response.Body.Close()
	}

	cookieOptions := func() *websocket.DialOptions {
		return &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": []string{session.cookie.String()}}}
	}
	connection, _, err = websocket.Dial(ctx, wsURL+"/ws/events", cookieOptions())
	if err != nil {
		t.Fatalf("websocket with session cookie: %v", err)
	}
	waitFor(t, func() bool { return fixture.server.Events().SubscriberCount() == 1 })
	_ = connection.Close(websocket.StatusNormalClosure, "")

	connection, _, err = websocket.Dial(ctx, wsURL+"/ws/channel/any", cookieOptions())
	if err != nil {
		t.Fatalf("channel websocket with session cookie: %v", err)
	}
	_ = connection.Close(websocket.StatusNormalClosure, "")
}

func TestExposedListenWithoutAccountsFailsClosed(t *testing.T) {
	config := exposedConfig(t)
	if _, err := New(config); err == nil {
		t.Fatal("non-loopback listen without account service was accepted")
	}

	loopback := testConfig(t, false)
	loopback.SyncObjects = nil
	if _, err := New(loopback); err == nil {
		t.Fatal("missing sync object handler and account service was accepted")
	}
}

// M165: 账号会话经 /rpc 下发时, 处理器同时拿到 server 私有 identity 与 ipc 共享 userID;
// 无会话/CSRF 缺失的原鉴权行为不变。
func TestRequireAuthAccountSessionInjectsSharedUserID(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	err := fixture.server.dispatcher.RegisterRaw("test_echo_user", func(ctx context.Context, _ *ipc.Call) (any, error) {
		userID, ok := ipc.UserIDFromContext(ctx)
		role, roleOK := ipc.RoleFromContext(ctx)
		result := map[string]any{"userID": userID, "ok": ok, "role": role, "roleOK": roleOK}
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
	if data["roleOK"] != true || data["role"] != string(account.RoleSuperadmin) {
		t.Fatalf("injected role mismatch: %v", data)
	}

	anonymous := echo(nil, "")
	if anonymous.status != http.StatusUnauthorized {
		t.Fatalf("anonymous rpc status=%d, want 401", anonymous.status)
	}
	noCSRF := echo(session, "")
	if noCSRF.status != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d, want 403", noCSRF.status)
	}
	// 前端 isCsrfRejection 以 message 含 "CSRF" 识别拒绝并自动刷新重试, 关键字是契约。
	if message, _ := noCSRF.body["error"].(map[string]any)["message"].(string); !strings.Contains(message, "CSRF") {
		t.Fatalf("csrf rejection message=%q, want CSRF keyword", message)
	}
}
