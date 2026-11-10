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
	throttle.maxEntries = 1024
	blocked := "ip:192.0.2.7|user:victim"
	if !throttle.Allow(blocked) {
		t.Fatal("first attempt blocked")
	}
	throttle.RecordFailure(blocked)
	*now = now.Add(time.Second)
	for i := 0; i < throttle.maxEntries+10; i++ {
		throttle.Allow("flood:" + strconv.Itoa(i))
		*now = now.Add(time.Millisecond)
	}
	if throttle.Allow(blocked) {
		t.Fatal("blocked key was evicted by unrelated keys and regained its attempts")
	}
}

// fillWithBlocked fills the table with guards serving a backoff for keys
// ending in address, the way a flood of throwaway usernames would.
func fillWithBlocked(throttle *LoginThrottle, address string) {
	for i := 0; len(throttle.entries) < throttle.maxEntries; i++ {
		key := "login|throwaway-" + strconv.Itoa(i) + "|" + address
		throttle.Allow(key)
		throttle.RecordFailure(key)
	}
}

// Flooding the table with keys that are themselves blocked must not evict an
// older blocked key either.
func TestLoginThrottleBlockedFloodKeepsBlockedKey(t *testing.T) {
	throttle, _ := testThrottle()
	throttle.maxEntries = 1024
	victim := "login|victim|192.0.2.7"
	throttle.Allow(victim)
	throttle.RecordFailure(victim)
	fillWithBlocked(throttle, "198.51.100.9")
	for i := 0; i < 100; i++ {
		key := "login|more-" + strconv.Itoa(i) + "|198.51.100.9"
		if throttle.Allow(key) {
			throttle.RecordFailure(key)
		}
	}
	if len(throttle.entries) > throttle.maxEntries {
		t.Fatalf("entries=%d exceed hard cap", len(throttle.entries))
	}
	if throttle.Allow(victim) {
		t.Fatal("blocked key regained its attempts after a flood of blocked keys")
	}
}

// Regression for the shared-overflow lockout: an attacker fills the table
// with blocked guards and then hammers the overflow with failures. Overflow
// guards count attempts only and are sharded by network, so a user arriving
// from another network is still admitted, and the attacker's own network only
// gets the policy's window.
func TestLoginThrottleOverflowHammerDoesNotLockOutOtherNetworks(t *testing.T) {
	throttle, now := testThrottle()
	throttle.maxEntries = 1024
	fillWithBlocked(throttle, "1.2.3.4")
	for i := 0; i < 50; i++ {
		key := "login|overflow-" + strconv.Itoa(i) + "|5.6.7.8"
		throttle.Allow(key)
		throttle.RecordFailure(key)
	}
	*now = now.Add(20 * time.Second)
	if !throttle.Peek("login|admin|9.9.9.9") || !throttle.Allow("login|admin|9.9.9.9") {
		t.Fatal("a user from another network was refused after the overflow was hammered")
	}
	if throttle.Allow("login|again|5.6.7.200") {
		t.Fatal("the hammering network got more than the policy window")
	}
	*now = now.Add(loginWindow)
	if !throttle.Allow("login|again|5.6.7.200") {
		t.Fatal("overflow failures held the hammering network's shard shut past its window")
	}
}

// A full table of protected guards routes new keys to overflow shards, which
// admit attempts under the policy's window rather than none, and never take
// table slots.
func TestLoginThrottleFullTableAdmitsNewKeysThroughOverflow(t *testing.T) {
	throttle, now := testThrottle()
	throttle.maxEntries = 4
	for i := 0; i < 4; i++ {
		key := "blocked-" + strconv.Itoa(i) + "|192.0.2.1"
		throttle.Allow(key)
		throttle.RecordFailure(key)
	}
	for i := 0; i < loginWindowLimit; i++ {
		if !throttle.Allow("new-" + strconv.Itoa(i) + "|203.0.113.5") {
			t.Fatalf("new key %d refused by a full table", i)
		}
	}
	if throttle.Allow("new-extra|203.0.113.6") {
		t.Fatal("overflow shard exceeded the policy window")
	}
	if len(throttle.entries) != 4 {
		t.Fatalf("entries=%d, overflow keys must not take table slots", len(throttle.entries))
	}
	*now = now.Add(loginBackoffBase)
	if !throttle.Allow("after-expiry|203.0.113.6") {
		t.Fatal("new key refused after the guards' backoffs expired")
	}
}

// A trusted client bypasses overflow shards but not a guard of its own.
func TestLoginThrottleOwnBypassesOverflowOnly(t *testing.T) {
	throttle, _ := testThrottle()
	throttle.maxEntries = 2
	throttle.Allow("own|192.0.2.1")
	throttle.RecordFailure("own|192.0.2.1")
	throttle.Allow("other|192.0.2.2")
	throttle.RecordFailure("other|192.0.2.2")
	for i := 0; i < loginWindowLimit; i++ {
		throttle.Allow("flood-" + strconv.Itoa(i) + "|203.0.113.5")
	}
	if throttle.Peek("known|203.0.113.5") {
		t.Fatal("overflow shard should be exhausted for strangers")
	}
	if !throttle.PeekOwn("known|203.0.113.5") || !throttle.AllowOwn("known|203.0.113.5") {
		t.Fatal("trusted client refused by an overflow shard")
	}
	if throttle.PeekOwn("own|192.0.2.1") || throttle.AllowOwn("own|192.0.2.1") {
		t.Fatal("trusted client bypassed its own backoff")
	}
}

func TestOverflowShardGroupsCoarseNetworks(t *testing.T) {
	if overflowShard("login|a|192.0.2.1") != overflowShard("login|b|192.0.2.250") {
		t.Fatal("one IPv4 /24 spread over shards")
	}
	if overflowShard("login|a|2001:db8:1:2::/64") != overflowShard("login|b|2001:db8:1:ffff::/64") {
		t.Fatal("one IPv6 /48 spread over shards")
	}
	distinct := map[int]bool{}
	for i := 0; i < 64; i++ {
		distinct[overflowShard("login|a|10."+strconv.Itoa(i)+".0.1")] = true
	}
	if len(distinct) < 32 {
		t.Fatalf("64 networks landed in only %d shards", len(distinct))
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
