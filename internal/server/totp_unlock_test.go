package server

import (
	"net/http"
	"testing"
	"time"
)

func (a *lockoutAudit) byKind(kind string) []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []map[string]any
	for i, k := range a.kind {
		if k == kind {
			out = append(out, a.payloads[i])
		}
	}
	return out
}

// enableTOTP binds a TOTP secret to the session's user and returns it.
func (f *accountFixture) enableTOTP(t *testing.T, session *accountTestSession) string {
	t.Helper()
	call := f.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("setup status=%d body=%v", call.status, call.body)
	}
	secret, _ := call.body["secret"].(string)
	call = f.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("confirm status=%d body=%v", call.status, call.body)
	}
	return secret
}

func TestMFALockoutClearDropsStateAndKeepsReservationsBalanced(t *testing.T) {
	l := newMFALockout()
	if l.clear("u1") {
		t.Fatal("clearing an unknown user must report nothing cleared")
	}
	for i := 0; i < mfaLockThreshold; i++ {
		l.begin("u1")
		l.failure("u1")
	}
	if l.begin("u1") {
		t.Fatal("user should be locked")
	}
	if !l.clear("u1") {
		t.Fatal("clear must report the dropped lock")
	}
	// A reservation made before the clear completes afterwards without reviving the lock.
	if !l.begin("u1") {
		t.Fatal("cleared user must be admitted")
	}
	if l.clear("u1") {
		t.Fatal("an in-flight reservation alone is not lock state")
	}
	l.release("u1")
	if _, _, level, locked := l.failure("u1"); locked || level != 0 {
		t.Fatalf("stale failure after clear locked=%v level=%d", locked, level)
	}
	for i := 0; i < mfaLockThreshold-1; i++ {
		if !l.begin("u1") {
			t.Fatalf("begin %d refused", i)
		}
		l.release("u1")
	}
}

func TestPasswordResetPathsClearSecondFactorLock(t *testing.T) {
	type env struct {
		fixture *accountFixture
		audit   *lockoutAudit
		clock   *lockoutClock
		userID  string
		secret  string
	}
	scenarios := []struct {
		name   string
		reason string
		admin  bool
		act    func(t *testing.T, e *env, session *accountTestSession, recoveryKey, adminID string, adminSession *accountTestSession)
	}{
		{name: "password change", reason: "password_change", act: func(t *testing.T, e *env, session *accountTestSession, _, _ string, _ *accountTestSession) {
			body, _, _ := envelopeBody(t, "password-n2")
			body = mergeBody(body, map[string]any{"old_password": "password-n1", "new_password": "password-n2"})
			if call := e.fixture.call(t, http.MethodPost, "/auth/password", body, session, session.csrf, nil); call.status != http.StatusOK {
				t.Fatalf("password change status=%d body=%v", call.status, call.body)
			}
		}},
		{name: "recovery reset", reason: "recovery_reset", act: func(t *testing.T, e *env, _ *accountTestSession, recoveryKey, _ string, _ *accountTestSession) {
			body, _, _ := envelopeBody(t, "password-n2")
			body = mergeBody(body, map[string]any{"username": "root", "recovery_key": recoveryKey, "new_password": "password-n2"})
			if call := e.fixture.call(t, http.MethodPost, "/auth/recovery/reset", body, nil, "", nil); call.status != http.StatusOK {
				t.Fatalf("recovery reset status=%d body=%v", call.status, call.body)
			}
		}},
		{name: "admin reset", reason: "admin_reset", admin: true, act: func(t *testing.T, e *env, _ *accountTestSession, _, _ string, adminSession *accountTestSession) {
			if call := e.fixture.call(t, http.MethodPost, "/admin/users/"+e.userID+"/reset", nil, adminSession, adminSession.csrf, nil); call.status != http.StatusOK {
				t.Fatalf("admin reset status=%d body=%v", call.status, call.body)
			}
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			fixture := newAccountFixture(t, AuthOn)
			e := &env{fixture: fixture, audit: &lockoutAudit{}, clock: &lockoutClock{}}
			fixture.server.mfaLock.now = e.clock.now
			fixture.server.audit = e.audit.record

			var session, adminSession *accountTestSession
			var recoveryKey, adminID string
			if sc.admin {
				adminSession, _ = fixture.initSuperadmin(t, "admin", "password-a0")
				adminID, _ = adminSession.user["id"].(string)
				if call := fixture.call(t, http.MethodPost, "/admin/users", map[string]any{"username": "root", "password": "password-n1"}, adminSession, adminSession.csrf, nil); call.status != http.StatusOK {
					t.Fatalf("create user status=%d body=%v", call.status, call.body)
				}
				session = fixture.login(t, "root", "password-n1")
			} else {
				session, recoveryKey = fixture.initSuperadmin(t, "root", "password-n1")
			}
			e.userID, _ = session.user["id"].(string)
			e.secret = fixture.enableTOTP(t, session)

			fixture.lockUser(t, e.userID)
			if call := fixture.totpLogin(t, fixture.freshTicket(e.userID), testTOTPCode(t, e.secret, time.Now().Unix())); call.status != http.StatusTooManyRequests {
				t.Fatalf("user should be locked, status=%d", call.status)
			}

			sc.act(t, e, session, recoveryKey, adminID, adminSession)

			unlocks := e.audit.byKind("totp_unlock")
			if len(unlocks) != 1 {
				t.Fatalf("totp_unlock audits=%d", len(unlocks))
			}
			p := unlocks[0]
			if p["user_id"] != e.userID || p["reason"] != sc.reason {
				t.Fatalf("unexpected unlock payload: %v", p)
			}
			if ip, _ := p["client_ip"].(string); ip == "" {
				t.Fatalf("client_ip missing: %v", p)
			}
			if sc.admin && p["admin_id"] != adminID {
				t.Fatalf("admin_id=%v want %v", p["admin_id"], adminID)
			}

			// A stale lock or failure count would surface as 429 or an early lock here.
			fixture.lockUser(t, e.userID)
			e.clock.advance(mfaLockDuration + time.Second)
			if sc.admin {
				// An administrator reset also deletes the TOTP binding, so a login cannot succeed;
				// the lockout itself must admit attempts again after the base duration.
				if !fixture.server.mfaLock.begin(e.userID) {
					t.Fatal("level must restart at the base duration")
				}
				return
			}
			if call := fixture.totpLogin(t, fixture.freshTicket(e.userID), testTOTPCode(t, e.secret, time.Now().Unix())); call.status != http.StatusOK {
				t.Fatalf("level must restart at the base duration, status=%d body=%v", call.status, call.body)
			}
		})
	}
}

func TestPasswordChangeWithoutLockWritesNoUnlockAudit(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	audit := &lockoutAudit{}
	fixture.server.audit = audit.record
	session, _ := fixture.initSuperadmin(t, "root", "password-n1")
	body, _, _ := envelopeBody(t, "password-n2")
	body = mergeBody(body, map[string]any{"old_password": "password-n1", "new_password": "password-n2"})
	if call := fixture.call(t, http.MethodPost, "/auth/password", body, session, session.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("password change status=%d body=%v", call.status, call.body)
	}
	if got := audit.byKind("totp_unlock"); len(got) != 0 {
		t.Fatalf("unexpected unlock audits: %v", got)
	}
}
