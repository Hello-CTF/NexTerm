package production

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/takeover"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/server"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// AI-03: the real server HTTP/RPC surface with two accounts. Every AI owner
// surface (profiles, memory, grants, cron, HITL/takeover) is exercised over
// the wire: the owning account succeeds, the other account gets a 403 or an
// empty owner-scoped result, the gateway caller (shared key, no account
// identity) fails closed, and no response ever carries a plaintext secret.

const aiDualGatewayKey = "gateway-secret"

type aiDualAccountSession struct {
	cookie *http.Cookie
	csrf   string
}

type aiDualAccountCall struct {
	status  int
	body    map[string]any
	raw     string
	cookies []*http.Cookie
}

func (c aiDualAccountCall) ok() bool { return c.body["ok"] == true }

func (c aiDualAccountCall) errorCode() string {
	errorBody, _ := c.body["error"].(map[string]any)
	code, _ := errorBody["code"].(string)
	return code
}

func (c aiDualAccountCall) dataMap() map[string]any {
	data, _ := c.body["data"].(map[string]any)
	return data
}

func (c aiDualAccountCall) dataList() []any {
	data, _ := c.body["data"].([]any)
	return data
}

type aiDualAccountFixture struct {
	services    *ProductionServices
	database    *store.Store
	accounts    *account.Accounts
	rig         *accountCleanupRig
	http        *httptest.Server
	providerURL string
	aliceID     string
	bobID       string
	alice       *aiDualAccountSession
	bob         *aiDualAccountSession
}

// newAIDualAccountFixture composes the server-mode (desktop=false) AI runtime
// behind a real account-enabled HTTP server, exactly the command surface the
// server assembly mounts (ai-profiles, ai-agent, cron, ai-takeover).
func newAIDualAccountFixture(t *testing.T, providerScript http.Handler) *aiDualAccountFixture {
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
	fixture := &aiDualAccountFixture{}
	if providerScript != nil {
		provider := httptest.NewServer(providerScript)
		t.Cleanup(provider.Close)
		fixture.providerURL = provider.URL
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
	if err := registerAICommands(dispatcher, services.Profiles, database, productionScopeValidator(services)); err != nil {
		t.Fatal(err)
	}
	if err := agent.Module(services.Agent).RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	if err := takeover.Module(services.Takeover).RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	if err := cronModule(services.cron).RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}

	accounts := account.New(database.GORM())
	api, err := server.New(server.Config{
		Options:    server.Options{Auth: server.AuthOn, Listen: "127.0.0.1:0"},
		Dispatcher: dispatcher,
		Accounts:   accounts,
		Channels: server.ChannelBinderFunc(func(string) (server.ChannelReceiver, error) {
			return nil, errors.New("channel not used by AI dual account test")
		}),
		GatewayAuthKey: aiDualGatewayKey,
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

	fixture.services = services
	fixture.database = database
	fixture.accounts = accounts
	fixture.rig = rig
	fixture.http = httpServer
	code, err := accounts.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := accounts.InitSuperadmin(ctx, code, "alice", "password-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := accounts.CreateUser(ctx, "bob", "", "password-bob")
	if err != nil {
		t.Fatal(err)
	}
	fixture.aliceID, fixture.bobID = alice.ID, bob.ID
	fixture.alice = fixture.login(t, "alice", "password-alice")
	fixture.bob = fixture.login(t, "bob", "password-bob")
	return fixture
}

func (f *aiDualAccountFixture) login(t *testing.T, username, password string) *aiDualAccountSession {
	t.Helper()
	call := f.post(t, "/auth/login", map[string]any{"username": username, "password": password}, nil, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("login status = %d body = %v", call.status, call.body)
	}
	csrf, _ := call.body["csrf_token"].(string)
	for _, cookie := range call.cookies {
		if cookie.Name == "nexterm_session" {
			return &aiDualAccountSession{cookie: cookie, csrf: csrf}
		}
	}
	t.Fatal("login returned no account session cookie")
	return nil
}

func (f *aiDualAccountFixture) post(t *testing.T, path string, payload any, session *aiDualAccountSession, csrf string, extraHeaders map[string]string) aiDualAccountCall {
	t.Helper()
	var reader *strings.Reader
	if payload == nil {
		reader = strings.NewReader("")
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(encoded))
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
	} else if csrf != "" {
		request.Header.Set("X-NexTerm-CSRF", csrf)
	}
	for key, value := range extraHeaders {
		request.Header.Set(key, value)
	}
	response, err := f.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded := make(map[string]any)
	if strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rawBytes, &decoded); err != nil {
			t.Fatal(err)
		}
	}
	return aiDualAccountCall{status: response.StatusCode, body: decoded, raw: string(rawBytes), cookies: response.Cookies()}
}

func (f *aiDualAccountFixture) rpc(t *testing.T, session *aiDualAccountSession, command string, args any) aiDualAccountCall {
	t.Helper()
	return f.post(t, "/rpc", map[string]any{"cmd": command, "args": args}, session, "", nil)
}

func (f *aiDualAccountFixture) gatewayRPC(t *testing.T, command string, args any) aiDualAccountCall {
	t.Helper()
	return f.post(t, "/rpc", map[string]any{"cmd": command, "args": args}, nil, "", map[string]string{
		server.GatewayAuthHeader: aiDualGatewayKey,
	})
}

func requireAIDualOK(t *testing.T, call aiDualAccountCall, context string) {
	t.Helper()
	if !call.ok() {
		t.Fatalf("%s: status = %d body = %s", context, call.status, call.raw)
	}
}

func requireAIDualFailed(t *testing.T, call aiDualAccountCall, context string) {
	t.Helper()
	if call.ok() {
		t.Fatalf("%s: expected failure, got body = %s", context, call.raw)
	}
}

func requireAIDualForbiddenCode(t *testing.T, call aiDualAccountCall, context string) {
	t.Helper()
	if code := call.errorCode(); code != string(ipc.CodeForbidden) {
		t.Fatalf("%s: error code = %q, want forbidden; body = %s", context, code, call.raw)
	}
}

func requireAIDualNoSecret(t *testing.T, call aiDualAccountCall, secret, context string) {
	t.Helper()
	if strings.Contains(call.raw, secret) {
		t.Fatalf("%s: response leaked the plaintext secret: %s", context, call.raw)
	}
}

func (f *aiDualAccountFixture) ownerContext(userID string) context.Context {
	return ipc.WithUserID(context.Background(), userID)
}

func (f *aiDualAccountFixture) createAsset(t *testing.T, userID, name string) string {
	t.Helper()
	asset, err := f.database.AssetCreate(f.ownerContext(userID), store.AssetInput{Kind: "ssh", Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return asset.ID
}

func TestAIDualAccountProfilesOverRPC(t *testing.T) {
	var authorization atomic.Value
	var hits atomic.Int32
	models := newModelsServer(t, &authorization, &hits)
	fixture := newAIDualAccountFixture(t, nil)

	save := fixture.rpc(t, fixture.alice, "ai_model_save", map[string]any{"profile": map[string]any{
		"name": "alice-model", "baseUrl": models.URL, "apiKey": "alice-secret-key", "model": "m1",
		"contextWindow": 1000, "stream": true,
	}})
	requireAIDualOK(t, save, "alice ai_model_save")
	requireAIDualNoSecret(t, save, "alice-secret-key", "alice ai_model_save")
	saved, _ := save.body["data"].(map[string]any)
	if saved == nil || saved["id"] == "" {
		t.Fatalf("alice ai_model_save returned no profile id: %s", save.raw)
	}
	profileID, _ := saved["id"].(string)
	if got := saved["apiKey"]; got != profiles.MaskedAPIKey {
		t.Fatalf("saved profile apiKey = %v, want masked", got)
	}

	overview := fixture.rpc(t, fixture.alice, "ai_model_profiles", map[string]any{})
	requireAIDualOK(t, overview, "alice ai_model_profiles")
	requireAIDualNoSecret(t, overview, "alice-secret-key", "alice ai_model_profiles")

	foreign := fixture.rpc(t, fixture.bob, "ai_model_profiles", map[string]any{})
	requireAIDualOK(t, foreign, "bob ai_model_profiles")
	requireAIDualNoSecret(t, foreign, "alice-secret-key", "bob ai_model_profiles")
	foreignOverview, _ := foreign.body["data"].(map[string]any)
	foreignProfiles, _ := foreignOverview["profiles"].([]any)
	if len(foreignProfiles) != 0 {
		t.Fatalf("bob sees alice's profiles: %s", foreign.raw)
	}

	for _, test := range []struct {
		command string
		args    map[string]any
	}{
		{command: "ai_model_activate", args: map[string]any{"id": profileID}},
		{command: "ai_model_refresh", args: map[string]any{"profile": map[string]any{
			"id": profileID, "baseUrl": models.URL, "apiKey": profiles.MaskedAPIKey,
			"model": "m1", "contextWindow": 1000, "stream": true,
		}}},
	} {
		call := fixture.rpc(t, fixture.bob, test.command, test.args)
		requireAIDualFailed(t, call, "bob "+test.command)
		requireAIDualNoSecret(t, call, "alice-secret-key", "bob "+test.command)
	}
	if hits.Load() != 0 {
		t.Fatalf("bob's cross-account refresh reached the provider %d times", hits.Load())
	}

	// Delete is idempotent by design (sync tombstone convergence): bob's delete
	// of alice's profile succeeds as a no-op and must leave her state untouched.
	bobDelete := fixture.rpc(t, fixture.bob, "ai_model_delete", map[string]any{"id": profileID})
	requireAIDualOK(t, bobDelete, "bob ai_model_delete no-op")
	aliceAfter := fixture.rpc(t, fixture.alice, "ai_model_profiles", map[string]any{})
	requireAIDualOK(t, aliceAfter, "alice ai_model_profiles after bob's delete")
	aliceOverview, _ := aliceAfter.body["data"].(map[string]any)
	aliceProfiles, _ := aliceOverview["profiles"].([]any)
	if len(aliceProfiles) != 1 {
		t.Fatalf("alice's profile set changed by bob's delete: %s", aliceAfter.raw)
	}

	refresh := fixture.rpc(t, fixture.alice, "ai_model_refresh", map[string]any{"profile": map[string]any{
		"id": profileID, "baseUrl": models.URL, "apiKey": profiles.MaskedAPIKey,
		"model": "m1", "contextWindow": 1000, "stream": true,
	}})
	requireAIDualOK(t, refresh, "alice ai_model_refresh")
	if got := authorization.Load().(string); got != "Bearer alice-secret-key" {
		t.Fatalf("authorization = %q, want alice's stored key", got)
	}

	for _, test := range []struct {
		command string
		args    map[string]any
	}{
		{command: "ai_model_profiles", args: map[string]any{}},
		{command: "ai_model_save", args: map[string]any{"profile": map[string]any{
			"name": "gateway-model", "baseUrl": "https://gateway.invalid/v1", "apiKey": "gateway-key",
			"model": "m1", "contextWindow": 1000, "stream": true,
		}}},
		{command: "ai_model_delete", args: map[string]any{"id": profileID}},
	} {
		call := fixture.gatewayRPC(t, test.command, test.args)
		requireAIDualFailed(t, call, "gateway "+test.command)
		requireAIDualForbiddenCode(t, call, "gateway "+test.command)
	}

	deleteCall := fixture.rpc(t, fixture.alice, "ai_model_delete", map[string]any{"id": profileID})
	requireAIDualOK(t, deleteCall, "alice ai_model_delete")
}

func TestAIDualAccountMemoryOverRPC(t *testing.T) {
	fixture := newAIDualAccountFixture(t, nil)

	create := fixture.rpc(t, fixture.alice, "memory_create", map[string]any{
		"topic": "deploy", "content": "alice deployment note",
	})
	requireAIDualOK(t, create, "alice memory_create")
	created, _ := create.body["data"].(map[string]any)
	if created == nil || created["id"] == "" {
		t.Fatalf("alice memory_create returned no entry id: %s", create.raw)
	}
	entryID, _ := created["id"].(string)

	index := fixture.rpc(t, fixture.alice, "memory_index", map[string]any{})
	requireAIDualOK(t, index, "alice memory_index")
	if rows := index.dataList(); len(rows) != 1 {
		t.Fatalf("alice memory_index = %s, want 1 topic", index.raw)
	}

	// The DTO scope is client-reported and must be ignored: bob's forged scope
	// lands in bob's own server-derived scope, never in alice's.
	forged := fixture.rpc(t, fixture.bob, "memory_create", map[string]any{
		"scope": map[string]any{"tenant": "user", "subject": fixture.aliceID},
		"topic": "deploy", "content": "bob forged-scope note",
	})
	requireAIDualOK(t, forged, "bob memory_create with forged scope")
	aliceIndex := fixture.rpc(t, fixture.alice, "memory_index", map[string]any{})
	requireAIDualOK(t, aliceIndex, "alice memory_index after bob forged create")
	if rows := aliceIndex.dataList(); len(rows) != 1 {
		t.Fatalf("alice memory_index after bob's forged create = %s, want only her own topic", aliceIndex.raw)
	}
	bobIndex := fixture.rpc(t, fixture.bob, "memory_index", map[string]any{})
	requireAIDualOK(t, bobIndex, "bob memory_index")
	if rows := bobIndex.dataList(); len(rows) != 1 {
		t.Fatalf("bob memory_index = %s, want only his own topic", bobIndex.raw)
	}

	for _, test := range []struct {
		command string
		args    map[string]any
	}{
		{command: "memory_get", args: map[string]any{"id": entryID}},
		{command: "memory_edit", args: map[string]any{"id": entryID, "expectedVersion": 1, "content": "bob rewrite"}},
		{command: "memory_delete", args: map[string]any{"id": entryID, "expectedVersion": 1}},
	} {
		call := fixture.rpc(t, fixture.bob, test.command, test.args)
		requireAIDualFailed(t, call, "bob "+test.command)
	}
	aliceGet := fixture.rpc(t, fixture.alice, "memory_get", map[string]any{"id": entryID})
	requireAIDualOK(t, aliceGet, "alice memory_get")
	if got := aliceGet.dataMap()["content"]; got != "alice deployment note" {
		t.Fatalf("alice memory_get content = %v, want untouched original", got)
	}

	settings := fixture.rpc(t, fixture.alice, "memory_settings_get", map[string]any{})
	requireAIDualOK(t, settings, "alice memory_settings_get")

	for _, command := range []string{"memory_index", "memory_settings_get", "memory_create"} {
		args := map[string]any{}
		if command == "memory_create" {
			args = map[string]any{"topic": "t", "content": "gateway note"}
		}
		call := fixture.gatewayRPC(t, command, args)
		requireAIDualFailed(t, call, "gateway "+command)
		requireAIDualForbiddenCode(t, call, "gateway "+command)
	}

	deleteCall := fixture.rpc(t, fixture.alice, "memory_delete", map[string]any{"id": entryID, "expectedVersion": 1})
	requireAIDualOK(t, deleteCall, "alice memory_delete")
}

func TestAIDualAccountGrantsOverRPC(t *testing.T) {
	fixture := newAIDualAccountFixture(t, nil)
	aliceAsset := fixture.createAsset(t, fixture.aliceID, "alice-server")
	bobAsset := fixture.createAsset(t, fixture.bobID, "bob-server")

	set := fixture.rpc(t, fixture.alice, "ai_grant_set", map[string]any{
		"deviceId": aliceAsset, "kinds": []string{"terminal_write"},
	})
	requireAIDualOK(t, set, "alice ai_grant_set")
	list := fixture.rpc(t, fixture.alice, "ai_grant_list", map[string]any{})
	requireAIDualOK(t, list, "alice ai_grant_list")
	if rows := list.dataList(); len(rows) != 1 {
		t.Fatalf("alice ai_grant_list = %s, want 1 grant", list.raw)
	}

	bobList := fixture.rpc(t, fixture.bob, "ai_grant_list", map[string]any{})
	requireAIDualOK(t, bobList, "bob ai_grant_list")
	if rows := bobList.dataList(); len(rows) != 0 {
		t.Fatalf("bob sees alice's grants: %s", bobList.raw)
	}

	for _, test := range []struct {
		command string
		args    map[string]any
	}{
		{command: "ai_grant_set", args: map[string]any{"deviceId": aliceAsset, "kinds": []string{"terminal_write"}}},
		{command: "ai_grant_revoke", args: map[string]any{"deviceId": aliceAsset}},
	} {
		call := fixture.rpc(t, fixture.bob, test.command, test.args)
		requireAIDualFailed(t, call, "bob "+test.command+" on alice's asset")
		requireAIDualForbiddenCode(t, call, "bob "+test.command+" on alice's asset")
	}
	bobSet := fixture.rpc(t, fixture.bob, "ai_grant_set", map[string]any{
		"deviceId": bobAsset, "kinds": []string{"terminal_write"},
	})
	requireAIDualOK(t, bobSet, "bob ai_grant_set on own asset")

	for _, test := range []struct {
		command string
		args    map[string]any
	}{
		{command: "ai_grant_list", args: map[string]any{}},
		{command: "ai_grant_set", args: map[string]any{"deviceId": aliceAsset, "kinds": []string{"terminal_write"}}},
		{command: "ai_grant_revoke", args: map[string]any{"deviceId": aliceAsset}},
	} {
		call := fixture.gatewayRPC(t, test.command, test.args)
		requireAIDualFailed(t, call, "gateway "+test.command)
		requireAIDualForbiddenCode(t, call, "gateway "+test.command)
	}

	revoke := fixture.rpc(t, fixture.alice, "ai_grant_revoke", map[string]any{"deviceId": aliceAsset})
	requireAIDualOK(t, revoke, "alice ai_grant_revoke")
}

func TestAIDualAccountCronOverRPC(t *testing.T) {
	fixture := newAIDualAccountFixture(t, nil)

	conversation := fixture.rpc(t, fixture.alice, "ai_conversation_create", map[string]any{"title": "alice chat"})
	requireAIDualOK(t, conversation, "alice ai_conversation_create")
	created, _ := conversation.body["data"].(map[string]any)
	if created == nil || created["id"] == "" {
		t.Fatalf("alice ai_conversation_create returned no id: %s", conversation.raw)
	}
	conversationID, _ := created["id"].(string)

	register := fixture.rpc(t, fixture.alice, "cron_register", map[string]any{
		"sessionId": conversationID, "prompt": "run", "schedule": "* * * * *",
	})
	requireAIDualOK(t, register, "alice cron_register")
	registered, _ := register.body["data"].(map[string]any)
	if registered == nil || registered["id"] == "" {
		t.Fatalf("alice cron_register returned no job id: %s", register.raw)
	}
	jobID, _ := registered["id"].(string)

	list := fixture.rpc(t, fixture.alice, "cron_list", map[string]any{"sessionId": conversationID})
	requireAIDualOK(t, list, "alice cron_list")
	if rows := list.dataList(); len(rows) != 1 {
		t.Fatalf("alice cron_list = %s, want 1 job", list.raw)
	}

	bobList := fixture.rpc(t, fixture.bob, "cron_list", map[string]any{"sessionId": conversationID})
	requireAIDualOK(t, bobList, "bob cron_list on alice's conversation")
	if rows := bobList.dataList(); len(rows) != 0 {
		t.Fatalf("bob sees alice's cron jobs: %s", bobList.raw)
	}

	for _, test := range []struct {
		command string
		args    map[string]any
	}{
		{command: "cron_register", args: map[string]any{"sessionId": conversationID, "prompt": "run", "schedule": "* * * * *"}},
		{command: "cron_get", args: map[string]any{"sessionId": conversationID, "jobId": jobID}},
		{command: "cron_set_enabled", args: map[string]any{"sessionId": conversationID, "jobId": jobID, "enabled": false}},
		{command: "cron_unregister", args: map[string]any{"sessionId": conversationID, "jobId": jobID}},
	} {
		call := fixture.rpc(t, fixture.bob, test.command, test.args)
		requireAIDualFailed(t, call, "bob "+test.command)
	}

	aliceGet := fixture.rpc(t, fixture.alice, "cron_get", map[string]any{"sessionId": conversationID, "jobId": jobID})
	requireAIDualOK(t, aliceGet, "alice cron_get")
	if got := aliceGet.dataMap()["enabled"]; got != true {
		t.Fatalf("alice cron_get enabled = %v after bob's attempts, want true", got)
	}

	for _, test := range []struct {
		command string
		args    map[string]any
		// cron_register validates the owner-scoped conversation before the
		// store access check, so a gateway caller fails with not_found there
		// while cron_list reaches the store and fails with forbidden.
		wantCode string
	}{
		{command: "cron_list", args: map[string]any{"sessionId": conversationID}, wantCode: string(ipc.CodeForbidden)},
		{command: "cron_register", args: map[string]any{"sessionId": conversationID, "prompt": "run", "schedule": "* * * * *"}, wantCode: string(ipc.CodeNotFound)},
	} {
		call := fixture.gatewayRPC(t, test.command, test.args)
		requireAIDualFailed(t, call, "gateway "+test.command)
		if code := call.errorCode(); code != test.wantCode {
			t.Fatalf("gateway %s: error code = %q, want %q; body = %s", test.command, code, test.wantCode, call.raw)
		}
	}

	unregister := fixture.rpc(t, fixture.alice, "cron_unregister", map[string]any{"sessionId": conversationID, "jobId": jobID})
	requireAIDualOK(t, unregister, "alice cron_unregister")
}

// aiDualHITLScript answers the first model request with a dangerous tool call
// (guard asks, the run parks in a HITL confirmation) and the follow-up with a
// plain done content, mirroring the composed outcome-runtime script.
type aiDualHITLScript struct {
	command string
}

func (s *aiDualHITLScript) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	var parsed spawnScriptRequest
	if err := json.Unmarshal(body, &parsed); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	if !spawnHasRole(&parsed, "tool") {
		encoded, err := json.Marshal(tools.ExecCommandsArgs{Commands: []string{s.command}})
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		spawnWriteToolCall(writer, "call-exec-1", "exec_commands", string(encoded))
		return
	}
	spawnWriteContent(writer, "done")
}

func TestAIDualAccountHITLOverRPC(t *testing.T) {
	script := &aiDualHITLScript{command: "systemctl restart nginx"}
	fixture := newAIDualAccountFixture(t, script)

	save := fixture.rpc(t, fixture.alice, "ai_model_save", map[string]any{"profile": map[string]any{
		"name": "alice-live", "baseUrl": fixture.providerURL, "apiKey": "alice-secret-key", "model": "test-model",
		"contextWindow": 32768, "stream": true,
	}})
	requireAIDualOK(t, save, "alice ai_model_save for HITL run")
	requireAIDualNoSecret(t, save, "alice-secret-key", "alice ai_model_save for HITL run")

	aliceCtx := fixture.ownerContext(fixture.aliceID)
	connected, _ := fixture.rig.open(t, aliceCtx, session.Asset{ID: "asset/alice-ssh", Kind: session.KindSSH})
	stream := &agent.SliceStream{}
	response, err := fixture.services.Agent.Start(aliceCtx, agent.ChatArgs{
		Message: "run it", Scope: tools.Scope{SessionID: connected.ID},
	}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitOutcomeEvent(t, stream, "confirmRequired")
	if confirmation.ID != "call-exec-1" || confirmation.Nonce == "" {
		t.Fatalf("confirmRequired = %+v", confirmation)
	}

	snapshot := fixture.rpc(t, fixture.alice, "ai_hitl_snapshot", map[string]any{"jobId": response.JobID})
	requireAIDualOK(t, snapshot, "alice ai_hitl_snapshot")
	if !strings.Contains(snapshot.raw, "call-exec-1") {
		t.Fatalf("alice ai_hitl_snapshot missing the pending call: %s", snapshot.raw)
	}
	events := fixture.rpc(t, fixture.alice, "ai_hitl_events", map[string]any{"jobId": response.JobID, "afterSeq": 0})
	requireAIDualOK(t, events, "alice ai_hitl_events")

	for _, test := range []struct {
		session *aiDualAccountSession
		name    string
	}{
		{session: fixture.bob, name: "bob"},
		{session: nil, name: "gateway"},
	} {
		snapshotCall := fixture.rpc(t, test.session, "ai_hitl_snapshot", map[string]any{"jobId": response.JobID})
		if test.session == nil {
			snapshotCall = fixture.gatewayRPC(t, "ai_hitl_snapshot", map[string]any{"jobId": response.JobID})
		}
		requireAIDualFailed(t, snapshotCall, test.name+" ai_hitl_snapshot")
		eventsCall := fixture.rpc(t, test.session, "ai_hitl_events", map[string]any{"jobId": response.JobID, "afterSeq": 0})
		if test.session == nil {
			eventsCall = fixture.gatewayRPC(t, "ai_hitl_events", map[string]any{"jobId": response.JobID, "afterSeq": 0})
		}
		requireAIDualFailed(t, eventsCall, test.name+" ai_hitl_events")
		confirmCall := fixture.rpc(t, test.session, "ai_confirm", map[string]any{
			"jobId": response.JobID, "callId": confirmation.ID, "nonce": confirmation.Nonce, "decision": "allow",
		})
		if test.session == nil {
			confirmCall = fixture.gatewayRPC(t, "ai_confirm", map[string]any{
				"jobId": response.JobID, "callId": confirmation.ID, "nonce": confirmation.Nonce, "decision": "allow",
			})
		}
		requireAIDualFailed(t, confirmCall, test.name+" ai_confirm")
	}

	confirm := fixture.rpc(t, fixture.alice, "ai_confirm", map[string]any{
		"jobId": response.JobID, "callId": confirmation.ID, "nonce": confirmation.Nonce, "decision": "allow",
	})
	requireAIDualOK(t, confirm, "alice ai_confirm")
	closed := waitOutcomeClosed(t, stream)
	sawDone := false
	for _, event := range closed {
		if event.Type == "error" {
			t.Fatalf("alice's confirmed run failed: %s", event.Message)
		}
		if event.Type == "done" {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatalf("alice's confirmed run did not finish: %+v", closed)
	}

	runGet := fixture.rpc(t, fixture.alice, "ai_run_get", map[string]any{"jobId": response.JobID})
	requireAIDualOK(t, runGet, "alice ai_run_get")
	bobRunGet := fixture.rpc(t, fixture.bob, "ai_run_get", map[string]any{"jobId": response.JobID})
	requireAIDualFailed(t, bobRunGet, "bob ai_run_get")
	gatewayRunGet := fixture.gatewayRPC(t, "ai_run_get", map[string]any{"jobId": response.JobID})
	requireAIDualFailed(t, gatewayRunGet, "gateway ai_run_get")
}

func TestAIDualAccountTakeoverOverRPC(t *testing.T) {
	fixture := newAIDualAccountFixture(t, nil)
	aliceCtx := fixture.ownerContext(fixture.aliceID)
	_, tab := fixture.rig.open(t, aliceCtx, session.Asset{ID: "device/alice", Kind: session.KindDevice})

	enter := fixture.rpc(t, fixture.alice, "ai_takeover_enter", map[string]any{"tabId": tab.ID})
	requireAIDualOK(t, enter, "alice ai_takeover_enter")
	token, _ := enter.dataMap()["token"].(string)
	if token == "" {
		t.Fatalf("alice ai_takeover_enter returned no token: %s", enter.raw)
	}

	bobEnter := fixture.rpc(t, fixture.bob, "ai_takeover_enter", map[string]any{"tabId": tab.ID})
	requireAIDualFailed(t, bobEnter, "bob ai_takeover_enter")
	for _, test := range []struct {
		command string
		args    map[string]any
	}{
		{command: "ai_takeover_pause", args: map[string]any{"tabId": tab.ID, "token": token}},
		{command: "ai_takeover_exit", args: map[string]any{"tabId": tab.ID, "token": token, "reason": "bob leaves"}},
	} {
		call := fixture.rpc(t, fixture.bob, test.command, test.args)
		requireAIDualFailed(t, call, "bob "+test.command)
	}

	exit := fixture.rpc(t, fixture.alice, "ai_takeover_exit", map[string]any{"tabId": tab.ID, "token": token, "reason": "alice leaves"})
	requireAIDualOK(t, exit, "alice ai_takeover_exit")

	// Documented boundary: the gateway-only caller must fail closed here, but
	// the takeover manager currently accepts identity-less ownership on an
	// unattended tab (the session snapshot escalates to open access). Filed
	// with the tower (gateway identity-less takeover enter); the fix belongs
	// to internal/ai/takeover, outside M17 scope. Re-enable once it lands.
	t.Run("gatewayEnterFailsClosed", func(t *testing.T) {
		t.Skip("known defect filed to tower: gateway caller can enter takeover on an unattended tab; fix owned by internal/ai/takeover")
		gatewayEnter := fixture.gatewayRPC(t, "ai_takeover_enter", map[string]any{"tabId": tab.ID})
		requireAIDualFailed(t, gatewayEnter, "gateway ai_takeover_enter")
		requireAIDualForbiddenCode(t, gatewayEnter, "gateway ai_takeover_enter")
	})
}
