package server

import (
	"net/http"
	"testing"
	"time"
)

func TestTOTPSetupConfirmDisableAreRejectedWhileLocked(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	clock := &lockoutClock{}
	fixture.server.mfaLock.now = clock.now
	userID, secret, session, recovery := fixture.mfaUserSession(t, "root")
	fixture.lockUser(t, userID)
	code := testTOTPCode(t, secret, time.Now().Unix())

	for _, tc := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPost, "/auth/totp/setup", map[string]any{"reverify": code}},
		{http.MethodPost, "/auth/totp/setup", map[string]any{"reverify": recovery[0]}},
		{http.MethodPost, "/auth/totp/confirm", map[string]any{"code": code}},
		{http.MethodDelete, "/auth/totp", map[string]any{"code": code}},
		{http.MethodDelete, "/auth/totp", map[string]any{"code": recovery[1]}},
	} {
		call := fixture.call(t, tc.method, tc.path, tc.body, session, session.csrf, nil)
		if call.status != http.StatusTooManyRequests {
			t.Fatalf("%s during lock status=%d body=%v", tc.path, call.status, call.body)
		}
	}
	status := fixture.call(t, http.MethodGet, "/auth/totp", nil, session, "", nil)
	if status.body["enabled"] != true || status.body["recovery_codes_left"] != float64(len(recovery)) {
		t.Fatalf("locked requests changed state: %v", status.body)
	}

	clock.advance(mfaLockDuration + time.Second)
	if call := fixture.call(t, http.MethodDelete, "/auth/totp", map[string]any{"code": recovery[1]}, session, session.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("disable after lock status=%d body=%v", call.status, call.body)
	}
}

func TestTOTPSetupWrongReverifyCountsTowardLock(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	userID, _, session, _ := fixture.mfaUserSession(t, "root")
	call := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{"reverify": "wrong-password"}, session, session.csrf, nil)
	if call.status != http.StatusForbidden {
		t.Fatalf("wrong reverify status=%d body=%v", call.status, call.body)
	}
	fixture.server.mfaLock.mu.Lock()
	entry := fixture.server.mfaLock.entries[userID]
	fails := 0
	if entry != nil {
		fails = entry.fails
	}
	fixture.server.mfaLock.mu.Unlock()
	if fails != 1 {
		t.Fatalf("shared lock failures=%d, want 1", fails)
	}
}
