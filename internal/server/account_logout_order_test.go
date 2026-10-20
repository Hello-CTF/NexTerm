package server

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestAccountActiveLogoutCleanupCredentialOrder(t *testing.T) {
	for _, test := range []struct {
		name              string
		cleanupFirst      bool
		wantCleanupStatus int
	}{
		{name: "cleanup before revoke succeeds", cleanupFirst: true, wantCleanupStatus: http.StatusOK},
		{name: "revoke before cleanup is rejected", cleanupFirst: false, wantCleanupStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &accountCleanupRecorder{}
			fixture := newAccountCleanupFixture(t, recorder.record)
			alice, _ := fixture.initSuperadmin(t, "alice", "password-alice")
			aliceID := alice.user["id"].(string)
			var cleanupCalls atomic.Int32
			if err := fixture.server.dispatcher.RegisterRaw("test_active_logout_cleanup", func(ctx context.Context, _ *ipc.Call) (any, error) {
				if err := ipc.CheckAccountSession(ctx); err != nil {
					return nil, err
				}
				ownerID, ok := ipc.UserIDFromContext(ctx)
				if !ok || ownerID != aliceID {
					return nil, ipc.NewError(ipc.CodeForbidden, "unexpected cleanup owner")
				}
				cleanupCalls.Add(1)
				return map[string]bool{"cleaned": true}, nil
			}); err != nil {
				t.Fatal(err)
			}
			cleanup := func() accountCall {
				return fixture.call(t, http.MethodPost, "/rpc", map[string]any{
					"cmd": "test_active_logout_cleanup", "args": map[string]any{},
				}, alice, alice.csrf, nil)
			}
			var call accountCall
			if test.cleanupFirst {
				call = cleanup()
				if call.status != http.StatusOK || call.body["ok"] != true {
					t.Fatalf("pre-logout cleanup status = %d body = %v", call.status, call.body)
				}
			}
			logout := fixture.call(t, http.MethodPost, "/auth/logout", nil, alice, alice.csrf, nil)
			if logout.status != http.StatusOK {
				t.Fatalf("logout status = %d body = %v", logout.status, logout.body)
			}
			if !test.cleanupFirst {
				call = cleanup()
			}
			if call.status != test.wantCleanupStatus {
				t.Fatalf("cleanup status = %d body = %v, want %d", call.status, call.body, test.wantCleanupStatus)
			}
			if test.cleanupFirst {
				afterRevoke := cleanup()
				if afterRevoke.status != http.StatusUnauthorized {
					t.Fatalf("cleanup after revoke status = %d body = %v", afterRevoke.status, afterRevoke.body)
				}
			}
			if got := cleanupCalls.Load(); got != 1 && test.cleanupFirst || got != 0 && !test.cleanupFirst {
				t.Fatalf("cleanup handler calls = %d, cleanupFirst = %v", got, test.cleanupFirst)
			}
			requireAccountCleanupOwners(t, recorder, aliceID)
		})
	}
}
