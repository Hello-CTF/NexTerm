package server

import (
	"net/http"
	"testing"
	"time"
)

func TestMFALockoutReservationsCountTowardThreshold(t *testing.T) {
	lockout := newMFALockout()
	for i := 0; i < mfaLockThreshold; i++ {
		if !lockout.begin("u1") {
			t.Fatalf("begin %d refused below the threshold", i)
		}
	}
	// Ten verifications are in flight and none finished: no further one may start.
	if lockout.begin("u1") {
		t.Fatal("begin admitted an attempt beyond the threshold while reservations are outstanding")
	}
	lockout.release("u1")
	if !lockout.begin("u1") {
		t.Fatal("a released reservation must free a slot")
	}
}

func TestTOTPLockedRecoveryCodeIsRejectedAndNotConsumed(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	clock := &lockoutClock{}
	fixture.server.mfaLock.now = clock.now
	userID, _, _, recovery := fixture.mfaUserSession(t, "root")
	fixture.lockUser(t, userID)

	call := fixture.totpLogin(t, fixture.freshTicket(userID), recovery[0])
	if call.status != http.StatusTooManyRequests || len(call.cookies) != 0 {
		t.Fatalf("valid recovery code during lock status=%d body=%v", call.status, call.body)
	}
	clock.advance(mfaLockDuration + time.Second)
	call = fixture.totpLogin(t, fixture.freshTicket(userID), recovery[0])
	if call.status != http.StatusOK {
		t.Fatalf("recovery code must still work after the lock: status=%d body=%v", call.status, call.body)
	}
}
