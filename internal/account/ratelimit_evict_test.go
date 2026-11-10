package account

import (
	"strconv"
	"testing"
	"time"
)

// Flooding the guard table with unrelated keys must not clear the lockout of a
// key that is currently blocked.
func TestLoginThrottleFloodDoesNotEvictBlockedKey(t *testing.T) {
	throttle, now := testThrottle()
	blocked := "ip:192.0.2.7|user:victim"
	if !throttle.Allow(blocked) {
		t.Fatal("first attempt blocked")
	}
	throttle.RecordFailure(blocked)
	*now = now.Add(time.Second)
	for i := 0; i < loginGuardMaxEntries+10; i++ {
		throttle.Allow("flood:" + strconv.Itoa(i))
		*now = now.Add(time.Millisecond)
	}
	if throttle.Allow(blocked) {
		t.Fatal("blocked key was evicted by unrelated keys and regained its attempts")
	}
}

// Flooding the table with keys that are themselves blocked (one failure per
// throwaway username) must not evict an older blocked key either. Once every
// guard is protected, new keys share one overflow guard: they are limited
// together instead of all being refused.
func TestLoginThrottleBlockedFloodKeepsBlockedKeyAndSharesOverflow(t *testing.T) {
	throttle, now := testThrottle()
	victim := "login|victim|192.0.2.7"
	throttle.Allow(victim)
	throttle.RecordFailure(victim)
	for i := 0; i < loginGuardMaxEntries+10; i++ {
		key := "login|throwaway-" + strconv.Itoa(i) + "|192.0.2.7"
		if throttle.Allow(key) {
			throttle.RecordFailure(key)
		}
	}
	if len(throttle.entries) > loginGuardMaxEntries {
		t.Fatalf("entries=%d exceed hard cap", len(throttle.entries))
	}
	if throttle.Allow(victim) {
		t.Fatal("blocked key regained its attempts after a flood of blocked keys")
	}
	// The flood's own overflow failures put the shared guard in backoff.
	if throttle.Peek("login|fresh|192.0.2.8") {
		t.Fatal("overflow guard admitted a new key while serving the flood's backoff")
	}
	*now = now.Add(loginBackoffMax)
	for i := 0; i < loginWindowLimit; i++ {
		if !throttle.Allow("login|fresh-" + strconv.Itoa(i) + "|192.0.2.8") {
			t.Fatalf("new key %d refused after the backoffs expired", i)
		}
	}
}

// A full table of protected guards routes new keys to the shared overflow
// guard, which admits attempts under the policy's limit rather than none.
func TestLoginThrottleFullTableAdmitsNewKeysThroughOverflow(t *testing.T) {
	throttle, now := testThrottle()
	throttle.maxEntries = 4
	for i := 0; i < 4; i++ {
		key := "blocked-" + strconv.Itoa(i)
		throttle.Allow(key)
		throttle.RecordFailure(key)
	}
	for i := 0; i < loginWindowLimit; i++ {
		if !throttle.Allow("new-" + strconv.Itoa(i)) {
			t.Fatalf("new key %d refused by a full table", i)
		}
	}
	if throttle.Allow("new-extra") {
		t.Fatal("overflow guard exceeded the policy window")
	}
	if len(throttle.entries) != 4 {
		t.Fatalf("entries=%d, overflow keys must not take table slots", len(throttle.entries))
	}
	*now = now.Add(loginBackoffBase)
	if !throttle.Allow("after-expiry") {
		t.Fatal("new key refused after the guards' backoffs expired")
	}
}

// Peek neither spends an attempt nor creates a guard.
func TestLoginThrottlePeekHasNoSideEffects(t *testing.T) {
	throttle, _ := testThrottle()
	for i := 0; i < loginWindowLimit*3; i++ {
		if !throttle.Peek("user:alice") {
			t.Fatal("peek refused an unused key")
		}
	}
	if len(throttle.entries) != 0 {
		t.Fatalf("peek created %d guards", len(throttle.entries))
	}
	for i := 0; i < loginWindowLimit; i++ {
		if !throttle.Allow("user:alice") {
			t.Fatalf("attempt %d refused after peeks", i+1)
		}
	}
	if throttle.Peek("user:alice") {
		t.Fatal("peek admitted a key with an exhausted window")
	}
}

// The idle sweep must not drop a guard that is still serving a backoff.
func TestLoginThrottleSweepKeepsBlockedIdleGuard(t *testing.T) {
	throttle, now := testThrottle()
	victim := "login|victim|192.0.2.7"
	for round := 0; round < 7; round++ {
		throttle.Allow(victim)
		throttle.RecordFailure(victim)
		*now = now.Add(loginBackoffBase << round)
	}
	// The last failure set a 32-minute backoff; stay idle past the idle TTL.
	throttle.Allow(victim)
	throttle.RecordFailure(victim)
	for i := 0; i < loginGuardMaxEntries/2+1; i++ {
		throttle.Allow("filler:" + strconv.Itoa(i))
	}
	*now = now.Add(loginGuardIdleTTL + time.Minute)
	throttle.Allow("trigger-sweep")
	if _, ok := throttle.entries[victim]; !ok {
		t.Fatal("idle sweep dropped a guard that is still serving a backoff")
	}
	if throttle.Allow(victim) {
		t.Fatal("blocked key admitted after the idle sweep")
	}
}
