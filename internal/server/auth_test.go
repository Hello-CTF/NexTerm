package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
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
	fixture := newAccountFixtureListen(t, AuthOn, "0.0.0.0:8080")
	fixture.server.gatewayAuthKey = testGatewayKey
	url := fixture.http.URL + "/rpc"

	status, body := postRPC(t, fixture.client, url, "app_info", map[string]string{GatewayAuthHeader: testGatewayKey})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("gateway-only non-owner admission = %d %+v", status, body)
	}
	status, body = postRPC(t, fixture.client, url, "app_info", map[string]string{GatewayAuthHeader: "wrong"})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("wrong gateway key = %d %+v", status, body)
	}
	status, body = postRPC(t, fixture.client, url, "app_info", map[string]string{"X-HC-User-ID": "forged"})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("forged platform identity header = %d %+v", status, body)
	}
	status, body = postRPC(t, fixture.client, url, "app_info", map[string]string{"X-HC-User-ID": "forged", GatewayAuthHeader: "wrong"})
	if status != http.StatusUnauthorized || body.OK {
		t.Fatalf("forged platform header with wrong gateway key = %d %+v", status, body)
	}

	session, _ := fixture.initSuperadmin(t, "root", "password-rpc-gateway")
	call := fixture.call(t, http.MethodPost, "/rpc", map[string]any{"cmd": "app_info", "args": map[string]any{}}, session, session.csrf, map[string]string{GatewayAuthHeader: testGatewayKey})
	if call.status != http.StatusOK {
		t.Fatalf("gateway plus account session = %d body=%v", call.status, call.body)
	}

	connection, response, err := websocket.Dial(context.Background(), strings.Replace(fixture.http.URL, "http", "ws", 1)+"/ws/events", &websocket.DialOptions{
		HTTPHeader: http.Header{GatewayAuthHeader: []string{testGatewayKey}},
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

	connection, response, err = websocket.Dial(ctx, wsURL+"/ws/channel/any", cookieOptions())
	if err == nil {
		_ = connection.Close(websocket.StatusNormalClosure, "")
		t.Fatal("unknown channel was accepted for an account session")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("unknown channel response = %+v, want 403", response)
	}
	if response != nil {
		response.Body.Close()
	}
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

// M165: when an account session is delivered via /rpc, the handler gets both the server-private identity and the ipc-shared userID;
// the original auth behavior without a session/with missing CSRF is unchanged.
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
	// The frontend isCsrfRejection recognizes rejections by the "CSRF" keyword in the message and auto-refreshes/retries; the keyword is a contract.
	if message, _ := noCSRF.body["error"].(map[string]any)["message"].(string); !strings.Contains(message, "CSRF") {
		t.Fatalf("csrf rejection message=%q, want CSRF keyword", message)
	}
}

func TestAIPermissionScopeUsesRequestAccountOverHTTP(t *testing.T) {
	type runtimeFixture struct {
		account     *accountFixture
		sessions    *session.Manager
		legacyCalls *atomic.Int64
	}
	newRuntime := func(t *testing.T, auth string, serverMode bool) *runtimeFixture {
		t.Helper()
		fixture := newAccountFixture(t, auth)
		manager := session.NewManager(session.Config{IdleTimeout: -1})
		storage, err := store.OpenInMemory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		permissions, err := guard.NewManager(context.Background(), storage)
		if err != nil {
			t.Fatal(err)
		}
		deps := tools.WithSession(tools.Dependencies{}, manager)
		legacyTabSession := deps.TabSession
		legacyCalls := &atomic.Int64{}
		deps.TabSession = func(tabID string) string {
			legacyCalls.Add(1)
			return legacyTabSession(tabID)
		}
		runner := agent.NewRunner(agent.Config{
			Tools: tools.NewRegistry(deps), Permissions: permissions, Store: storage, ServerMode: serverMode,
		})
		if err := runner.RegisterCommands(fixture.server.dispatcher); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = runner.Close()
			_ = manager.Close()
			_ = storage.Close()
		})
		return &runtimeFixture{account: fixture, sessions: manager, legacyCalls: legacyCalls}
	}
	followGlobal := func(t *testing.T, runtime *runtimeFixture, actor *accountTestSession, tab session.TabInfo) accountCall {
		t.Helper()
		return runtime.account.call(t, http.MethodPost, "/rpc", map[string]any{
			"cmd": "ai_set_permission",
			"args": map[string]any{
				"scope":        map[string]any{"tabId": tab.ID},
				"followGlobal": true,
			},
		}, actor, func() string {
			if actor == nil {
				return ""
			}
			return actor.csrf
		}(), nil)
	}

	t.Run("two accounts and same-account peer", func(t *testing.T) {
		runtime := newRuntime(t, AuthOn, true)
		runtime.account.createUser(t, "alice", "password-alice")
		runtime.account.createUser(t, "bob", "password-bob")
		alice := runtime.account.login(t, "alice", "password-alice")
		alicePeer := runtime.account.login(t, "alice", "password-alice")
		bob := runtime.account.login(t, "bob", "password-bob")
		aliceID, _ := alice.user["id"].(string)
		tab, err := runtime.sessions.RegisterRemoteSession(ipc.WithUserID(context.Background(), aliceID), session.RemoteSessionOptions{
			OwnerID: aliceID, AssetID: "alice-asset", Title: "alice-terminal",
		})
		if err != nil {
			t.Fatal(err)
		}
		if call := followGlobal(t, runtime, alice, tab); call.status != http.StatusOK || call.body["ok"] != true {
			t.Fatalf("owner AI scope = %d %+v", call.status, call.body)
		}
		if call := followGlobal(t, runtime, bob, tab); call.status != http.StatusOK || call.body["ok"] != false {
			t.Fatalf("cross-account AI scope = %d %+v", call.status, call.body)
		} else if errorBody, _ := call.body["error"].(map[string]any); errorBody["code"] != "bad_param" {
			t.Fatalf("cross-account AI scope error = %+v", call.body)
		}
		if call := followGlobal(t, runtime, alicePeer, tab); call.status != http.StatusOK || call.body["ok"] != true {
			t.Fatalf("same-account peer AI scope = %d %+v", call.status, call.body)
		}
		if calls := runtime.legacyCalls.Load(); calls != 0 {
			t.Fatalf("HTTP AI scope used unscoped TabSession %d times", calls)
		}
	})

	t.Run("desktop open access", func(t *testing.T) {
		runtime := newRuntime(t, AuthOff, false)
		tab, err := runtime.sessions.RegisterRemoteSession(context.Background(), session.RemoteSessionOptions{
			OwnerID: "desktop", AssetID: "desktop-asset", Title: "desktop-terminal",
		})
		if err != nil {
			t.Fatal(err)
		}
		if call := followGlobal(t, runtime, nil, tab); call.status != http.StatusOK || call.body["ok"] != true {
			t.Fatalf("open AI scope = %d %+v", call.status, call.body)
		}
		if calls := runtime.legacyCalls.Load(); calls != 0 {
			t.Fatalf("open AI scope used unscoped TabSession %d times", calls)
		}
	})
}

type aiJobAuthModel struct {
	block bool
	first *schema.Message
	calls atomic.Int64
}

func (m *aiJobAuthModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if m.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if m.calls.Add(1) == 1 && m.first != nil {
		return m.first, nil
	}
	return schema.AssistantMessage("done", nil), nil
}

func (m *aiJobAuthModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if m.calls.Add(1) == 1 && m.first != nil {
		return schema.StreamReaderFromArray([]*schema.Message{m.first}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
}

type aiJobAuthStream struct {
	events chan map[string]any
	closed chan struct{}
	once   sync.Once
}

func newAIJobAuthStream() *aiJobAuthStream {
	return &aiJobAuthStream{events: make(chan map[string]any, 128), closed: make(chan struct{})}
}

func (s *aiJobAuthStream) SendJSON(ctx context.Context, raw json.RawMessage) error {
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	select {
	case <-s.closed:
		return ipc.ErrStreamsUnavailable
	case s.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *aiJobAuthStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

type aiJobAuthStreams struct {
	mu     sync.Mutex
	values map[string]*aiJobAuthStream
}

func (s *aiJobAuthStreams) open(_ context.Context, channel ipc.ChannelRef) (ipc.JSONStream, error) {
	stream := newAIJobAuthStream()
	s.mu.Lock()
	if s.values == nil {
		s.values = make(map[string]*aiJobAuthStream)
	}
	s.values[channel.ID] = stream
	s.mu.Unlock()
	return stream, nil
}

func (s *aiJobAuthStreams) wait(t *testing.T, channelID, kind string) map[string]any {
	t.Helper()
	s.mu.Lock()
	stream := s.values[channelID]
	s.mu.Unlock()
	if stream == nil {
		t.Fatalf("stream %q was not opened", channelID)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-stream.events:
			if event["type"] == kind {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s on %s", kind, channelID)
		}
	}
}

func TestAIJobOperationsAuthorizationOverHTTP(t *testing.T) {
	type runtime struct {
		fixture   *accountFixture
		runner    *agent.Runner
		store     *store.Store
		streams   *aiJobAuthStreams
		alice     *accountTestSession
		alicePeer *accountTestSession
		bob       *accountTestSession
		aliceCtx  context.Context
	}
	newRuntime := func(t *testing.T, chat model.BaseChatModel, deps tools.Dependencies, authMode ...string) *runtime {
		t.Helper()
		mode := AuthOn
		if len(authMode) > 0 {
			mode = authMode[0]
		}
		accountDB, err := store.OpenInMemory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = accountDB.Close() })
		database, err := store.OpenInMemory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		config := testConfig(t, false)
		config.Options.Auth = mode
		if mode == AuthPlatform {
			config.Options.Listen = "0.0.0.0:8080"
			config.GatewayAuthKey = testGatewayKey
		}
		accounts := account.New(accountDB.GORM())
		config.Accounts = accounts
		streams := &aiJobAuthStreams{}
		config.Environment.Streams = ipc.StreamFactoryFuncs{JSON: streams.open}
		runner := agent.NewRunner(agent.Config{
			Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
			Tools: tools.NewRegistry(deps), Store: database, Runs: database, ServerMode: true,
		})
		t.Cleanup(func() { _ = runner.Close() })
		if err := runner.RegisterCommands(config.Dispatcher); err != nil {
			t.Fatal(err)
		}
		server, httpServer := newTestHTTP(t, config)
		fixture := &accountFixture{server: server, http: httpServer, accounts: accounts, db: accountDB.DB(), client: httpServer.Client()}
		result := &runtime{fixture: fixture, runner: runner, store: database, streams: streams, aliceCtx: context.Background()}
		if mode == AuthOn {
			fixture.createUser(t, "alice", "password-alice")
			fixture.createUser(t, "bob", "password-bob")
			result.alice = fixture.login(t, "alice", "password-alice")
			result.alicePeer = fixture.login(t, "alice", "password-alice")
			result.bob = fixture.login(t, "bob", "password-bob")
			aliceID, _ := result.alice.user["id"].(string)
			result.aliceCtx = ipc.WithUserID(context.Background(), aliceID)
		}
		return result
	}
	rpc := func(t *testing.T, runtime *runtime, actor *accountTestSession, command string, args map[string]any, headers ...map[string]string) accountCall {
		t.Helper()
		csrf := ""
		if actor != nil {
			csrf = actor.csrf
		}
		var header map[string]string
		if len(headers) > 0 {
			header = headers[0]
		}
		return runtime.fixture.call(t, http.MethodPost, "/rpc", map[string]any{"cmd": command, "args": args}, actor, csrf, header)
	}
	start := func(t *testing.T, runtime *runtime, channelID string) agent.StartResponse {
		t.Helper()
		body := map[string]any{
			"cmd": "ai_chat", "channel": channelID,
			"args": map[string]any{"args": map[string]any{"message": "go", "scope": map[string]any{"sessionId": "session"}}},
		}
		csrf := ""
		if runtime.alice != nil {
			csrf = runtime.alice.csrf
		}
		call := runtime.fixture.call(t, http.MethodPost, "/rpc", body, runtime.alice, csrf, nil)
		if call.status != http.StatusOK || call.body["ok"] != true {
			t.Fatalf("start AI job = %d %+v", call.status, call.body)
		}
		encoded, err := json.Marshal(call.body["data"])
		if err != nil {
			t.Fatal(err)
		}
		var started agent.StartResponse
		if err := json.Unmarshal(encoded, &started); err != nil || started.JobID == "" {
			t.Fatalf("start response = %+v err=%v", call.body, err)
		}
		return started
	}
	denied := func(t *testing.T, call accountCall) {
		t.Helper()
		errorBody, _ := call.body["error"].(map[string]any)
		if call.status != http.StatusOK || call.body["ok"] != false || errorBody["code"] != string(ipc.CodeForbidden) {
			t.Fatalf("foreign operation = %d %+v, want forbidden", call.status, call.body)
		}
	}
	accepted := func(t *testing.T, call accountCall) {
		t.Helper()
		if call.status != http.StatusOK || call.body["ok"] != true {
			t.Fatalf("authorized operation = %d %+v", call.status, call.body)
		}
	}

	exercise := func(t *testing.T, authMode, operation string, foreign bool) {
		t.Helper()
		var actions atomic.Int64
		var chat model.BaseChatModel
		deps := tools.Dependencies{}
		switch operation {
		case "cancel", "steer":
			chat = &aiJobAuthModel{block: true}
		case "confirm":
			chat = &aiJobAuthModel{first: schema.AssistantMessage("", []schema.ToolCall{{
				ID: "call", Type: "function", Function: schema.FunctionCall{Name: "docker_control", Arguments: `{"container_id":"web","action":"start"}`},
			}})}
			deps.DockerAct = func(context.Context, string, string, string) error { actions.Add(1); return nil }
		case "answer":
			chat = &aiJobAuthModel{first: schema.AssistantMessage("", []schema.ToolCall{{
				ID: "ask", Type: "function", Function: schema.FunctionCall{Name: "ask_user", Arguments: `{"question":"continue?","options":["yes","no"]}`},
			}})}
		}
		runtime := newRuntime(t, chat, deps, authMode)
		started := start(t, runtime, operation)
		operator := runtime.alicePeer
		switch operation {
		case "cancel":
			if foreign {
				denied(t, rpc(t, runtime, runtime.bob, "ai_cancel", map[string]any{"jobId": started.JobID}))
				row, err := runtime.store.RunGet(runtime.aliceCtx, started.JobID)
				if err != nil || row.Status != store.RunStatusRunning {
					t.Fatalf("run after foreign cancel = %+v err=%v", row, err)
				}
			}
			accepted(t, rpc(t, runtime, operator, "ai_cancel", map[string]any{"jobId": started.JobID}))
			waitFor(t, func() bool {
				row, err := runtime.store.RunGet(runtime.aliceCtx, started.JobID)
				return err == nil && row.Status == store.RunStatusCanceled
			})
		case "steer":
			if foreign {
				denied(t, rpc(t, runtime, runtime.bob, "ai_steer", map[string]any{"jobId": started.JobID, "message": "foreign"}))
				messages, err := runtime.store.MsgList(runtime.aliceCtx, started.ConversationID)
				if err != nil || len(messages) != 1 {
					t.Fatalf("messages after foreign steer = %+v err=%v", messages, err)
				}
			}
			accepted(t, rpc(t, runtime, operator, "ai_steer", map[string]any{"jobId": started.JobID, "message": "peer"}))
			messages, err := runtime.store.MsgList(runtime.aliceCtx, started.ConversationID)
			if err != nil || len(messages) != 2 {
				t.Fatalf("messages after authorized steer = %+v err=%v", messages, err)
			}
			accepted(t, rpc(t, runtime, operator, "ai_cancel", map[string]any{"jobId": started.JobID}))
		case "confirm":
			confirmation := runtime.streams.wait(t, operation, "confirmRequired")
			args := map[string]any{"jobId": started.JobID, "callId": confirmation["id"], "nonce": confirmation["confirmationNonce"], "decision": "allow"}
			if foreign {
				denied(t, rpc(t, runtime, runtime.bob, "ai_confirm", args))
				snapshot, err := runtime.runner.HITLSnapshot(started.JobID)
				if err != nil || len(snapshot.Pending) != 1 || actions.Load() != 0 {
					t.Fatalf("state after foreign confirm = %+v actions=%d err=%v", snapshot, actions.Load(), err)
				}
			}
			accepted(t, rpc(t, runtime, operator, "ai_confirm", args))
			runtime.streams.wait(t, operation, "done")
			if actions.Load() != 1 {
				t.Fatalf("actions after authorized confirm = %d", actions.Load())
			}
		case "answer":
			question := runtime.streams.wait(t, operation, "questionRequired")
			args := map[string]any{"jobId": started.JobID, "callId": question["id"], "nonce": question["confirmationNonce"], "text": "yes"}
			if foreign {
				denied(t, rpc(t, runtime, runtime.bob, "ai_answer", args))
				snapshot, err := runtime.runner.HITLSnapshot(started.JobID)
				if err != nil || len(snapshot.Pending) != 1 {
					t.Fatalf("state after foreign answer = %+v err=%v", snapshot, err)
				}
			}
			accepted(t, rpc(t, runtime, operator, "ai_answer", args))
			runtime.streams.wait(t, operation, "done")
		}
	}
	for _, operation := range []string{"cancel", "steer", "confirm", "answer"} {
		t.Run(operation, func(t *testing.T) {
			t.Run("account", func(t *testing.T) { exercise(t, AuthOn, operation, true) })
			t.Run("anonymous "+AuthOff, func(t *testing.T) { exercise(t, AuthOff, operation, false) })
			t.Run("anonymous "+AuthLoopback, func(t *testing.T) { exercise(t, AuthLoopback, operation, false) })
		})
	}

	t.Run("gateway unknown identity", func(t *testing.T) {
		runtime := newRuntime(t, &aiJobAuthModel{block: true}, tools.Dependencies{}, AuthPlatform)
		stream := &agent.SliceStream{}
		started, err := runtime.runner.Start(ipc.WithUserID(context.Background(), "gateway-owner"), agent.ChatArgs{Message: "go"}, agent.StaticStream(stream))
		if err != nil {
			t.Fatal(err)
		}
		denied(t, rpc(t, runtime, nil, "ai_cancel", map[string]any{"jobId": started.JobID}, map[string]string{GatewayAuthHeader: testGatewayKey}))
		row, err := runtime.store.RunGet(context.Background(), started.JobID)
		if err != nil || row.Status != store.RunStatusRunning {
			t.Fatalf("run after gateway unknown cancel = %+v err=%v", row, err)
		}
		if err := runtime.runner.Cancel(started.JobID); err != nil {
			t.Fatal(err)
		}
	})
}
