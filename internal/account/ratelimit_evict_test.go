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
// throwaway username) must not evict an older blocked key either: once every
// guard is serving a backoff, new keys are refused instead.
func TestLoginThrottleBlockedFloodKeepsBlockedKeyAndRefusesNewKeys(t *testing.T) {
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
	if throttle.Allow("login|fresh|192.0.2.8") {
		t.Fatal("new key admitted while every guard is serving a backoff")
	}
	*now = now.Add(loginBackoffBase)
	if !throttle.Allow("login|fresh|192.0.2.8") {
		t.Fatal("new key still refused after the backoffs expired")
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
