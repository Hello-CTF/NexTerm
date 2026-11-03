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
)

type lockoutClock struct{ offset atomic.Int64 }

func (c *lockoutClock) now() time.Time          { return time.Now().Add(time.Duration(c.offset.Load())) }
func (c *lockoutClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

type lockoutAudit struct {
	mu       sync.Mutex
	source   []string
	kind     []string
	payloads []map[string]any
}

func (a *lockoutAudit) record(_ context.Context, source, kind string, payload map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.source = append(a.source, source)
	a.kind = append(a.kind, kind)
	a.payloads = append(a.payloads, payload)
	return nil
}

func (a *lockoutAudit) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.payloads)
}

// mfaUser creates a user with a bound TOTP secret and returns its id and secret.
func (f *accountFixture) mfaUser(t *testing.T, username string) (userID, secret string) {
	t.Helper()
	f.createUser(t, username, "password-a1")
	session := f.login(t, username, "password-a1")
	call := f.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("setup status=%d body=%v", call.status, call.body)
	}
	secret, _ = call.body["secret"].(string)
	call = f.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("confirm status=%d body=%v", call.status, call.body)
	}
	userID, _ = session.user["id"].(string)
	if userID == "" {
		t.Fatalf("missing user id: %v", session.user)
	}
	return userID, secret
}

func (f *accountFixture) totpLogin(t *testing.T, ticket, code string) accountCall {
	t.Helper()
	return f.call(t, http.MethodPost, "/auth/totp/login", map[string]any{"ticket": ticket, "code": code}, nil, "", nil)
}

func (f *accountFixture) freshTicket(userID string) string {
	ticket, _ := f.server.mfaTickets.issue(userID, "", "", f.server.accounts.Epoch(userID))
	return ticket
}

func TestTOTPLoginLocksUserAcrossTicketsAndRecovers(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	clock := &lockoutClock{}
	fixture.server.mfaLock.now = clock.now
	audit := &lockoutAudit{}
	fixture.server.audit = audit.record
	userID, secret := fixture.mfaUser(t, "root")

	const wrongCode = "000000"
	for i := 0; i < mfaLockThreshold; i++ {
		if call := fixture.totpLogin(t, fixture.freshTicket(userID), wrongCode); call.status != http.StatusForbidden {
			t.Fatalf("wrong code %d status=%d body=%v", i, call.status, call.body)
		}
	}
	call := fixture.totpLogin(t, fixture.freshTicket(userID), testTOTPCode(t, secret, time.Now().Unix()))
	if call.status != http.StatusTooManyRequests || len(call.cookies) != 0 {
		t.Fatalf("locked user with correct code status=%d body=%v", call.status, call.body)
	}
	if !strings.Contains(call.body["error"].(map[string]any)["message"].(string), "已暂时锁定") {
		t.Fatalf("lock message missing: %v", call.body)
	}

	clock.advance(mfaLockDuration + time.Second)
	// The account package verifies against the real clock; only the lockout clock is injected.
	call = fixture.totpLogin(t, fixture.freshTicket(userID), testTOTPCode(t, secret, time.Now().Unix()))
	if call.status != http.StatusOK {
		t.Fatalf("after lock expiry status=%d body=%v", call.status, call.body)
	}
}

func TestTOTPLoginLockRejectsRecoveryCodesToo(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	userID, _ := fixture.mfaUser(t, "root")
	for i := 0; i < mfaLockThreshold; i++ {
		fixture.totpLogin(t, fixture.freshTicket(userID), "000000")
	}
	// The lock must block before verification: an invalid-looking and a
	// well-formed recovery code get the same answer.
	for _, code := range []string{"aaaa-bbbb", "000000"} {
		if call := fixture.totpLogin(t, fixture.freshTicket(userID), code); call.status != http.StatusTooManyRequests {
			t.Fatalf("code %q status=%d", code, call.status)
		}
	}
}

func TestTOTPLockoutAuditsOnceWithoutCode(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	audit := &lockoutAudit{}
	fixture.server.audit = audit.record
	userID, _ := fixture.mfaUser(t, "root")

	const wrongCode = "918273"
	for i := 0; i < mfaLockThreshold+3; i++ {
		fixture.totpLogin(t, fixture.freshTicket(userID), wrongCode)
	}
	if audit.count() != 1 {
		t.Fatalf("audit calls=%d", audit.count())
	}
	audit.mu.Lock()
	defer audit.mu.Unlock()
	payload := audit.payloads[0]
	if payload["user_id"] != userID || payload["failures"] != mfaLockThreshold || payload["client_ip"] == "" || payload["locked_until"] == nil {
		t.Fatalf("unexpected payload: %v", payload)
	}
	encoded, _ := json.Marshal(payload)
	if strings.Contains(string(encoded), wrongCode) {
		t.Fatalf("payload leaks the submitted code: %s", encoded)
	}
	if audit.source[0] == "" || audit.kind[0] == "" {
		t.Fatalf("source=%q kind=%q", audit.source[0], audit.kind[0])
	}
}

func TestTOTPLockoutWithoutAuditFuncStillLocks(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.server.audit = nil
	userID, _ := fixture.mfaUser(t, "root")
	for i := 0; i < mfaLockThreshold; i++ {
		fixture.totpLogin(t, fixture.freshTicket(userID), "000000")
	}
	if call := fixture.totpLogin(t, fixture.freshTicket(userID), "000000"); call.status != http.StatusTooManyRequests {
		t.Fatalf("status=%d", call.status)
	}
}

func TestTOTPLockoutIsPerUserAndSuccessResets(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	aliceID, aliceSecret := fixture.mfaUser(t, "alice")
	bobID, bobSecret := fixture.mfaUser(t, "bob")

	for i := 0; i < mfaLockThreshold-1; i++ {
		fixture.totpLogin(t, fixture.freshTicket(aliceID), "000000")
	}
	if call := fixture.totpLogin(t, fixture.freshTicket(aliceID), testTOTPCode(t, aliceSecret, time.Now().Unix())); call.status != http.StatusOK {
		t.Fatalf("alice success status=%d body=%v", call.status, call.body)
	}
	// The success cleared the count: a full threshold of failures is needed again.
	for i := 0; i < mfaLockThreshold-1; i++ {
		fixture.totpLogin(t, fixture.freshTicket(aliceID), "000000")
	}
	if call := fixture.totpLogin(t, fixture.freshTicket(aliceID), "000000"); call.status != http.StatusForbidden {
		t.Fatalf("alice should not be locked yet, status=%d", call.status)
	}
	if call := fixture.totpLogin(t, fixture.freshTicket(aliceID), "000000"); call.status != http.StatusTooManyRequests {
		t.Fatalf("alice should be locked, status=%d", call.status)
	}
	if call := fixture.totpLogin(t, fixture.freshTicket(bobID), testTOTPCode(t, bobSecret, time.Now().Unix())); call.status != http.StatusOK {
		t.Fatalf("bob affected by alice lock, status=%d body=%v", call.status, call.body)
	}
}

func TestMFALockoutConcurrentFailures(t *testing.T) {
	lockout := newMFALockout()
	const workers = mfaLockThreshold * 5
	var admitted, rejected, crossings atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if !lockout.begin("u1") {
				rejected.Add(1)
				return
			}
			admitted.Add(1)
			if _, _, locked := lockout.failure("u1"); locked {
				crossings.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if admitted.Load() != mfaLockThreshold || rejected.Load() != workers-mfaLockThreshold {
		t.Fatalf("admitted=%d rejected=%d", admitted.Load(), rejected.Load())
	}
	if crossings.Load() != 1 {
		t.Fatalf("lock crossings=%d", crossings.Load())
	}
	if lockout.begin("u2") != true {
		t.Fatal("other user must not be locked")
	}
}

func TestMFALoginKeepsPasswordThrottleUntilSecondFactorPasses(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	_, secret := fixture.mfaUser(t, "root")
	body := map[string]any{"username": "root", "password": "password-a1"}

	var ticket string
	limited := false
	for i := 0; i < 20 && !limited; i++ {
		call := fixture.call(t, http.MethodPost, "/auth/login", body, nil, "", nil)
		switch call.status {
		case http.StatusOK:
			ticket, _ = call.body["ticket"].(string)
		case http.StatusTooManyRequests:
			limited = true
		default:
			t.Fatalf("login status=%d body=%v", call.status, call.body)
		}
	}
	if !limited {
		t.Fatal("password step never throttled although no second factor completed")
	}
	if call := fixture.totpLogin(t, ticket, testTOTPCode(t, secret, time.Now().Unix())); call.status != http.StatusOK {
		t.Fatalf("totp login status=%d body=%v", call.status, call.body)
	}
	if call := fixture.call(t, http.MethodPost, "/auth/login", body, nil, "", nil); call.status != http.StatusOK {
		t.Fatalf("login throttle not cleared after second factor, status=%d body=%v", call.status, call.body)
	}
}

func TestMFATicketCapRevokesOldestPerUser(t *testing.T) {
	tickets := newMFATicketStore()
	issued := make([]string, 0, mfaMaxTicketsPerUser+1)
	for i := 0; i < mfaMaxTicketsPerUser; i++ {
		ticket, _ := tickets.issue("u1", "", "", 0)
		issued = append(issued, ticket)
	}
	other, _ := tickets.issue("u2", "", "", 0)
	extra, _ := tickets.issue("u1", "", "", 0)
	issued = append(issued, extra)

	if _, ok := tickets.get(issued[0]); ok {
		t.Fatal("oldest ticket should be revoked")
	}
	for _, ticket := range issued[1:] {
		if _, ok := tickets.get(ticket); !ok {
			t.Fatal("newer ticket revoked")
		}
	}
	if _, ok := tickets.get(other); !ok {
		t.Fatal("other user's ticket revoked")
	}
}
