package server

import (
	"net/http"
	"testing"
	"time"
)

// mfaUserSession is mfaUser that also returns the logged-in session and the recovery codes.
func (f *accountFixture) mfaUserSession(t *testing.T, username string) (userID, secret string, session *accountTestSession, recovery []string) {
	t.Helper()
	f.createUser(t, username, "password-a1")
	session = f.login(t, username, "password-a1")
	call := f.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("setup status=%d body=%v", call.status, call.body)
	}
	secret, _ = call.body["secret"].(string)
	call = f.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("confirm status=%d body=%v", call.status, call.body)
	}
	for _, raw := range call.body["recovery_codes"].([]any) {
		recovery = append(recovery, raw.(string))
	}
	if len(recovery) == 0 {
		t.Fatal("no recovery codes")
	}
	userID, _ = session.user["id"].(string)
	return userID, secret, session, recovery
}

// fixedLockClock is a lockout clock that moves only when advanced.
type fixedLockClock struct{ t time.Time }

func (c *fixedLockClock) now() time.Time { return c.t }

func (c *fixedLockClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func (f *accountFixture) lockUser(t *testing.T, userID string) {
	t.Helper()
	for i := 0; i < mfaLockThreshold; i++ {
		if call := f.totpLogin(t, f.freshTicket(userID), "000000"); call.status != http.StatusForbidden {
			t.Fatalf("wrong code %d status=%d", i, call.status)
		}
	}
}

func TestMFALockDurationEscalatesAndCaps(t *testing.T) {
	want := []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 16 * time.Hour, mfaLockMaxDuration, mfaLockMaxDuration}
	for i, w := range want {
		if got := mfaLockDurationFor(i + 1); got != w {
			t.Fatalf("level %d duration=%v want %v", i+1, got, w)
		}
	}
	lockout := newMFALockout()
	clock := &fixedLockClock{t: time.Unix(1_700_000_000, 0)}
	lockout.now = clock.now
	for level := 1; level <= len(want); level++ {
		for i := 0; i < mfaLockThreshold; i++ {
			if !lockout.begin("u1") {
				t.Fatalf("level %d: begin refused at attempt %d", level, i)
			}
			_, until, gotLevel, locked := lockout.failure("u1")
			if locked != (i == mfaLockThreshold-1) {
				t.Fatalf("level %d attempt %d locked=%v", level, i, locked)
			}
			if locked {
				if gotLevel != level || until.Sub(clock.now()) != want[level-1] {
					t.Fatalf("level %d got level=%d lock=%v", level, gotLevel, until.Sub(clock.now()))
				}
			}
		}
		if lockout.begin("u1") {
			t.Fatalf("level %d: not locked", level)
		}
		clock.advance(want[level-1] + time.Second)
	}
}

func TestMFALockEscalationResetsOnSuccessAndDecays(t *testing.T) {
	lock := func(l *mfaLockout, clock *fixedLockClock) (level int, d time.Duration) {
		for i := 0; i < mfaLockThreshold; i++ {
			if !l.begin("u1") {
				t.Fatal("begin refused")
			}
			_, until, lvl, locked := l.failure("u1")
			if locked {
				level, d = lvl, until.Sub(clock.now())
			}
		}
		return level, d
	}
	clock := &fixedLockClock{t: time.Unix(1_700_000_000, 0)}
	l := newMFALockout()
	l.now = clock.now
	lock(l, clock)
	clock.advance(mfaLockDuration + time.Second)
	if level, _ := lock(l, clock); level != 2 {
		t.Fatalf("second lock level=%d", level)
	}
	clock.advance(2 * mfaLockDuration)
	if !l.begin("u1") {
		t.Fatal("begin refused after expiry")
	}
	l.success("u1")
	if level, d := lock(l, clock); level != 1 || d != mfaLockDuration {
		t.Fatalf("after success level=%d duration=%v", level, d)
	}

	// Without a success, a quiet period of mfaLockDecay after the lock ended also clears the escalation.
	clock = &fixedLockClock{t: time.Unix(1_700_000_000, 0)}
	l = newMFALockout()
	l.now = clock.now
	lock(l, clock)
	clock.advance(mfaLockDuration + mfaLockDecay + time.Second)
	if level, d := lock(l, clock); level != 1 || d != mfaLockDuration {
		t.Fatalf("after decay level=%d duration=%v", level, d)
	}
}

func TestTOTPLockoutEscalatesAndAuditsEachLockOnce(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	clock := &lockoutClock{}
	fixture.server.mfaLock.now = clock.now
	audit := &lockoutAudit{}
	fixture.server.audit = audit.record
	userID, _ := fixture.mfaUser(t, "root")

	fixture.lockUser(t, userID)
	clock.advance(mfaLockDuration + time.Second)
	fixture.lockUser(t, userID)
	// Still inside the doubled lock after the first duration has passed again.
	clock.advance(mfaLockDuration + time.Second)
	if call := fixture.totpLogin(t, fixture.freshTicket(userID), "000000"); call.status != http.StatusTooManyRequests {
		t.Fatalf("second lock must outlast the first duration, status=%d", call.status)
	}
	if audit.count() != 2 {
		t.Fatalf("audit calls=%d, want one per lock", audit.count())
	}
	audit.mu.Lock()
	defer audit.mu.Unlock()
	for i, wantLevel := range []int{1, 2} {
		payload := audit.payloads[i]
		if payload["lock_level"] != wantLevel || payload["lock_seconds"] != int64(mfaLockDurationFor(wantLevel)/time.Second) {
			t.Fatalf("audit %d payload=%v", i, payload)
		}
	}
}
