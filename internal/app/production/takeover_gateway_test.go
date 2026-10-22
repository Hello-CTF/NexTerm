package production

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/takeover"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/server"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// SEC01: the takeover owner gate over the real server HTTP/RPC surface. A
// gateway-authorized caller (shared key, no account session) must fail closed
// on ai_takeover_enter and every other takeover command: no banner injection,
// no empty-owner ownership, and no persistent lockout of the legitimate owner.
const takeoverGatewayAuthKey = "takeover-gateway-test"

type takeoverGatewaySession struct {
	cookie *http.Cookie
	csrf   string
}

type takeoverGatewayFixture struct {
	rig     *accountCleanupRig
	http    *httptest.Server
	aliceID string
	alice   *takeoverGatewaySession
	bob     *takeoverGatewaySession
}

func newTakeoverGatewayFixture(t *testing.T) *takeoverGatewayFixture {
	t.Helper()
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	database.SetAIHistoryShared(false)
	initTestVault(t, ctx, database)
	profileManager, err := profiles.NewManager(ctx, database, profiles.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	rig := newAccountCleanupRig(t)
	services := &ProductionServices{
		Store: database, Profiles: profileManager, Sessions: rig.manager,
		dataDir: t.TempDir(), desktop: false,
	}
	if err := composeAIRuntime(ctx, services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(services.closeAIRuntime)

	dispatcher := ipc.NewDispatcher()
	if err := takeover.Module(services.Takeover).RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	accounts := account.New(database.GORM())
	api, err := server.New(server.Config{
		Options:    server.Options{Auth: server.AuthOn, Listen: "127.0.0.1:0"},
		Dispatcher: dispatcher,
		Accounts:   accounts,
		Channels: server.ChannelBinderFunc(func(string) (server.ChannelReceiver, error) {
			return nil, errors.New("channel not used by takeover gateway test")
		}),
		GatewayAuthKey: takeoverGatewayAuthKey,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(api.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = api.Close()
	})

	fixture := &takeoverGatewayFixture{rig: rig, http: httpServer}
	code, err := accounts.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := accounts.InitSuperadmin(ctx, code, "alice", "password-alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.CreateUser(ctx, "bob", "", "password-bob"); err != nil {
		t.Fatal(err)
	}
	fixture.aliceID = alice.ID
	fixture.alice = fixture.login(t, "alice", "password-alice")
	fixture.bob = fixture.login(t, "bob", "password-bob")
	return fixture
}

func (f *takeoverGatewayFixture) login(t *testing.T, username, password string) *takeoverGatewaySession {
	t.Helper()
	status, body, cookies := f.post(t, "/auth/login", map[string]any{"username": username, "password": password}, nil, false)
	if status != http.StatusOK {
		t.Fatalf("login status = %d body = %s", status, body)
	}
	var decoded struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range cookies {
		if cookie.Name == "nexterm_session" {
			return &takeoverGatewaySession{cookie: cookie, csrf: decoded.CSRFToken}
		}
	}
	t.Fatal("login returned no account session cookie")
	return nil
}

func (f *takeoverGatewayFixture) post(t *testing.T, path string, payload any, session *takeoverGatewaySession, gateway bool) (int, []byte, []*http.Cookie) {
	t.Helper()
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(http.MethodPost, f.http.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if session != nil {
		request.AddCookie(session.cookie)
		request.Header.Set("X-NexTerm-CSRF", session.csrf)
	}
	if gateway {
		request.Header.Set(server.GatewayAuthHeader, takeoverGatewayAuthKey)
	}
	response, err := f.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body, response.Cookies()
}

func (f *takeoverGatewayFixture) rpc(t *testing.T, session *takeoverGatewaySession, command string, args map[string]any) ipc.Response {
	t.Helper()
	return f.rpcRequest(t, session, command, args)
}

func (f *takeoverGatewayFixture) gatewayRPC(t *testing.T, command string, args map[string]any) ipc.Response {
	t.Helper()
	return f.rpcRequest(t, nil, command, args)
}

func (f *takeoverGatewayFixture) rpcRequest(t *testing.T, session *takeoverGatewaySession, command string, args map[string]any) ipc.Response {
	t.Helper()
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	status, body, _ := f.post(t, "/rpc", ipc.Request{Command: command, Args: argsJSON}, session, session == nil)
	if status != http.StatusOK {
		t.Fatalf("%s status = %d body = %s", command, status, body)
	}
	var decoded ipc.Response
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("%s response is not an IPC envelope: %v", command, err)
	}
	return decoded
}

func requireTakeoverGatewayForbidden(t *testing.T, response ipc.Response, context string) {
	t.Helper()
	if response.OK {
		t.Fatalf("%s succeeded for an identity-less gateway caller", context)
	}
	if response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("%s error = %+v, want forbidden", context, response.Error)
	}
}

func (f *takeoverGatewayFixture) screenContains(t *testing.T, tabID, text string) bool {
	t.Helper()
	screen, err := f.rig.manager.ScreenText(tabID)
	if err != nil {
		t.Fatalf("screen of tab %s: %v", tabID, err)
	}
	return strings.Contains(screen, text)
}

func (f *takeoverGatewayFixture) enterTakeover(t *testing.T, session *takeoverGatewaySession, tabID string) string {
	t.Helper()
	response := f.rpc(t, session, "ai_takeover_enter", map[string]any{"tabId": tabID})
	if !response.OK {
		t.Fatalf("owner ai_takeover_enter = %+v", response.Error)
	}
	var entered takeover.EnterResponse
	if err := json.Unmarshal(response.Data, &entered); err != nil {
		t.Fatal(err)
	}
	if entered.Token == "" {
		t.Fatal("owner ai_takeover_enter returned no token")
	}
	return entered.Token
}

func (f *takeoverGatewayFixture) exitTakeover(t *testing.T, session *takeoverGatewaySession, tabID, token, reason string) {
	t.Helper()
	response := f.rpc(t, session, "ai_takeover_exit", map[string]any{"tabId": tabID, "token": token, "reason": reason})
	if !response.OK {
		t.Fatalf("owner ai_takeover_exit = %+v", response.Error)
	}
}

func TestProductionTakeoverGatewayIdentityLessFailsClosed(t *testing.T) {
	fixture := newTakeoverGatewayFixture(t)
	_, tab := fixture.rig.open(t, ipc.WithUserID(context.Background(), fixture.aliceID), session.Asset{ID: "device/alice", Kind: session.KindDevice})

	// M17: a gateway-only caller could enter any unattended tab, inject the
	// banner, and record an empty-owner ownership with no TTL that locked the
	// legitimate owner out until restart. The gate must reject the caller
	// before any of those side effects.
	requireTakeoverGatewayForbidden(t, fixture.gatewayRPC(t, "ai_takeover_enter", map[string]any{"tabId": tab.ID}), "gateway ai_takeover_enter")
	if fixture.screenContains(t, tab.ID, "AI 正在操作") {
		t.Fatal("identity-less gateway enter injected the takeover banner")
	}

	// The legitimate owner enters normally and the banner lands.
	token := fixture.enterTakeover(t, fixture.alice, tab.ID)
	if !fixture.screenContains(t, tab.ID, "AI 正在操作") {
		t.Fatal("legitimate owner enter did not inject the takeover banner")
	}

	// Every takeover command fails closed for the identity-less caller, even
	// with the owner's token, and none of them displaces the ownership.
	for _, call := range []struct {
		command string
		args    map[string]any
	}{
		{command: "ai_takeover_enter", args: map[string]any{"tabId": tab.ID}},
		{command: "ai_takeover_pause", args: map[string]any{"tabId": tab.ID, "token": token}},
		{command: "ai_takeover_exit", args: map[string]any{"tabId": tab.ID, "token": token, "reason": "seize"}},
	} {
		requireTakeoverGatewayForbidden(t, fixture.gatewayRPC(t, call.command, call.args), "gateway "+call.command)
	}

	// Cross-account (bob) cannot displace the owner either.
	if response := fixture.rpc(t, fixture.bob, "ai_takeover_enter", map[string]any{"tabId": tab.ID}); response.OK {
		t.Fatal("cross-account enter displaced the owner")
	}

	// The owner still holds the tab: exit succeeds, no empty-owner ownership
	// survives to block re-entry, and the gateway caller stays rejected.
	fixture.exitTakeover(t, fixture.alice, tab.ID, token, "alice leaves")
	requireTakeoverGatewayForbidden(t, fixture.gatewayRPC(t, "ai_takeover_enter", map[string]any{"tabId": tab.ID}), "gateway re-enter after owner exit")
	token = fixture.enterTakeover(t, fixture.alice, tab.ID)
	fixture.exitTakeover(t, fixture.alice, tab.ID, token, "done")
}
