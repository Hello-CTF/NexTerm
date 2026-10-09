package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/store"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
)

// syncV2EchoHandler 回显注入的同步用户身份, 用于验证 /sync/v2/* 中间件链。
func syncV2EchoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, _ := syncservice.UserIDFromContext(r.Context())
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"userId": userID, "path": r.URL.Path})
	})
}

func newSyncV2Fixture(t *testing.T) *accountFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := account.New(database.DB())
	server, httpServer := newTestHTTP(t, Config{
		Options: Options{
			Listen: "127.0.0.1:0", DataDir: t.TempDir(), Auth: AuthOn,
		},
		Dispatcher:  testDispatcher(t),
		Tokens:      TokenVerifierFunc(func(_ context.Context, token string) (bool, error) { return token == "secret", nil }),
		SyncObjects: syncV2EchoHandler(),
		Accounts:    accounts,
		Channels:    unavailableChannels(),
		Version:     "test-version",
		Logger:      testLogger(),
	})
	return &accountFixture{
		server: server, http: httpServer, accounts: accounts, db: database.DB(),
		client: &http.Client{},
	}
}

func TestSyncV2RoutesRequireSessionAndCSRF(t *testing.T) {
	fixture := newSyncV2Fixture(t)
	fixture.initSuperadmin(t, "alice", "alice-pw-123")

	for _, path := range []string{"/sync/v2/push", "/sync/v2/pull", "/sync/v2/ids"} {
		call := fixture.call(t, http.MethodPost, path, map[string]any{"protocol": 2}, nil, "", nil)
		if call.status != http.StatusUnauthorized {
			t.Fatalf("%s without session status=%d body=%v", path, call.status, call.body)
		}
	}

	session := fixture.login(t, "alice", "alice-pw-123")
	call := fixture.call(t, http.MethodPost, "/sync/v2/push", map[string]any{"protocol": 2}, session, "", nil)
	if call.status != http.StatusForbidden {
		t.Fatalf("push without CSRF status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodPost, "/sync/v2/push", map[string]any{"protocol": 2}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("push with session status=%d body=%v", call.status, call.body)
	}
	for _, path := range []string{"/sync/v2/pull", "/sync/v2/ids"} {
		call = fixture.call(t, http.MethodPost, path, map[string]any{"protocol": 2}, session, "", nil)
		if call.status != http.StatusOK {
			t.Fatalf("%s with session status=%d body=%v", path, call.status, call.body)
		}
	}
}

func TestSyncV2IdentityInjectionPerUser(t *testing.T) {
	fixture := newSyncV2Fixture(t)
	fixture.initSuperadmin(t, "alice", "alice-pw-123")
	if _, err := fixture.accounts.CreateUser(context.Background(), "bob", "bob", "bob-pw-123"); err != nil {
		t.Fatal(err)
	}

	alice := fixture.login(t, "alice", "alice-pw-123")
	bob := fixture.login(t, "bob", "bob-pw-123")
	aliceID := alice.user["id"].(string)
	bobID := bob.user["id"].(string)

	call := fixture.call(t, http.MethodPost, "/sync/v2/ids", map[string]any{"protocol": 2}, alice, "", nil)
	if call.body["userId"] != aliceID {
		t.Fatalf("alice echo = %+v, want userId %s", call.body, aliceID)
	}
	call = fixture.call(t, http.MethodPost, "/sync/v2/ids", map[string]any{"protocol": 2}, bob, "", nil)
	if call.body["userId"] != bobID {
		t.Fatalf("bob echo = %+v, want userId %s", call.body, bobID)
	}
}
