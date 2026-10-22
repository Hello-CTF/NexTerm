package server

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type accountCleanupRecorder struct {
	mu     sync.Mutex
	owners []string
}

func (r *accountCleanupRecorder) record(_ context.Context, ownerID, _ string, _ ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owners = append(r.owners, ownerID)
	return nil
}

func (r *accountCleanupRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.owners...)
}

func requireAccountCleanupOwners(t *testing.T, recorder *accountCleanupRecorder, want ...string) {
	t.Helper()
	got := recorder.snapshot()
	if len(got) != len(want) {
		t.Fatalf("account cleanup owners = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("account cleanup owners = %v, want %v", got, want)
		}
	}
}

func newAccountCleanupFixture(t *testing.T, cleanup AccountCleanupFunc) *accountFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := testConfig(t, false)
	config.Options.Auth = AuthOn
	config.Options.Listen = "127.0.0.1:0"
	accounts := account.New(database.GORM(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key")))
	config.Accounts = accounts
	config.AccountCleanup = cleanup
	config.AccountCleanupPrepare = func(ctx context.Context, ownerID, cleanupID string) (func(...string), func(), error) {
		return func(sessionIDs ...string) { _ = cleanup(ctx, ownerID, cleanupID, sessionIDs...) }, func() {}, nil
	}
	server, httpServer := newTestHTTP(t, config)
	return &accountFixture{
		server: server, http: httpServer, accounts: accounts, db: database.DB(), client: &http.Client{},
	}
}

func currentAccountSessionID(t *testing.T, fixture *accountFixture, session *accountTestSession) string {
	t.Helper()
	call := fixture.call(t, http.MethodGet, "/auth/sessions", nil, session, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("list sessions status = %d body = %v", call.status, call.body)
	}
	rows, ok := call.body["sessions"].([]any)
	if !ok {
		t.Fatalf("session list = %v", call.body)
	}
	for _, value := range rows {
		row := value.(map[string]any)
		if row["current"] == true {
			return row["id"].(string)
		}
	}
	t.Fatal("current account session not found")
	return ""
}

func TestAccountLifecycleCleanupHooks(t *testing.T) {
	tests := []struct {
		name string
		act  func(*testing.T, *accountFixture, *accountTestSession, map[string]string) accountCall
	}{
		{
			name: "logout",
			act: func(t *testing.T, fixture *accountFixture, session *accountTestSession, headers map[string]string) accountCall {
				return fixture.call(t, http.MethodPost, "/auth/logout", nil, session, session.csrf, headers)
			},
		},
		{
			name: "logout all",
			act: func(t *testing.T, fixture *accountFixture, session *accountTestSession, headers map[string]string) accountCall {
				return fixture.call(t, http.MethodPost, "/auth/logout-all", nil, session, session.csrf, headers)
			},
		},
		{
			name: "session revoke",
			act: func(t *testing.T, fixture *accountFixture, session *accountTestSession, headers map[string]string) accountCall {
				sessionID := currentAccountSessionID(t, fixture, session)
				return fixture.call(t, http.MethodDelete, "/auth/sessions/"+sessionID, nil, session, session.csrf, headers)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &accountCleanupRecorder{}
			fixture := newAccountCleanupFixture(t, recorder.record)
			alice, _ := fixture.initSuperadmin(t, "alice", "password-alice")
			if _, err := fixture.accounts.CreateUser(context.Background(), "bob", "", "password-bob"); err != nil {
				t.Fatal(err)
			}
			bob := fixture.login(t, "bob", "password-bob")
			fixture.login(t, "alice", "password-alice")
			aliceID := alice.user["id"].(string)
			bobID := bob.user["id"].(string)

			call := test.act(t, fixture, alice, map[string]string{"X-HC-User-ID": bobID})
			if call.status != http.StatusOK {
				t.Fatalf("account lifecycle status = %d body = %v", call.status, call.body)
			}
			requireAccountCleanupOwners(t, recorder, aliceID)
		})
	}
}

func TestAccountLifecycleCleanupRequiresValidCSRF(t *testing.T) {
	recorder := &accountCleanupRecorder{}
	fixture := newAccountCleanupFixture(t, recorder.record)
	alice, _ := fixture.initSuperadmin(t, "alice", "password-alice")
	call := fixture.call(t, http.MethodPost, "/auth/logout", nil, alice, "", nil)
	if call.status != http.StatusForbidden {
		t.Fatalf("logout without CSRF status = %d", call.status)
	}
	requireAccountCleanupOwners(t, recorder)
}

func TestAccountLifecycleCleanupAfterSessionRevalidationFails(t *testing.T) {
	recorder := &accountCleanupRecorder{}
	fixture := newAccountCleanupFixture(t, recorder.record)
	alice, _ := fixture.initSuperadmin(t, "alice", "password-alice")
	aliceID := alice.user["id"].(string)
	sessionID := currentAccountSessionID(t, fixture, alice)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	if err := fixture.server.dispatcher.RegisterRaw("test_account_session_recheck", func(ctx context.Context, _ *ipc.Call) (any, error) {
		close(entered)
		<-release
		return nil, ipc.CheckAccountSession(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	callDone := make(chan accountCall, 1)
	go func() {
		callDone <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
			"cmd": "test_account_session_recheck", "args": map[string]any{},
		}, alice, alice.csrf, nil)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("RPC did not reach the account session recheck")
	}
	if err := fixture.accounts.RevokeSession(context.Background(), sessionID); err != nil {
		t.Fatal(err)
	}
	requireAccountCleanupOwners(t, recorder)
	unblock()
	select {
	case call := <-callDone:
		if call.status != http.StatusOK || call.body["ok"] != false {
			t.Fatalf("stale session RPC status = %d body = %v", call.status, call.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale session RPC did not finish")
	}
	requireAccountCleanupOwners(t, recorder, aliceID)
}
