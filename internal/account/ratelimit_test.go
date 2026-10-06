package account

import (
	"strconv"
	"testing"
	"time"
)

func testThrottle() (*LoginThrottle, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	throttle := NewLoginThrottle()
	throttle.now = func() time.Time { return now }
	return throttle, &now
}

func TestLoginThrottleWindowLimit(t *testing.T) {
	throttle, now := testThrottle()
	key := "user:alice"
	for attempt := 0; attempt < loginWindowLimit; attempt++ {
		if !throttle.Allow(key) {
			t.Fatalf("attempt %d blocked", attempt+1)
		}
		*now = now.Add(2 * time.Second)
	}
	if throttle.Allow(key) {
		t.Fatal("6th attempt within window allowed")
	}
	*now = now.Add(loginWindow)
	if !throttle.Allow(key) {
		t.Fatal("attempt blocked after window rollover")
	}
}

func TestLoginThrottleBackoffDoublingAndCap(t *testing.T) {
	throttle, now := testThrottle()
	key := "ip:192.0.2.7"
	expected := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour}
	for round, want := range expected {
		if !throttle.Allow(key) {
			t.Fatalf("round %d blocked before failure", round)
		}
		throttle.RecordFailure(key)
		*now = now.Add(want - time.Second)
		if throttle.Allow(key) {
			t.Fatalf("round %d allowed during backoff", round)
		}
		*now = now.Add(time.Second)
	}
	if !throttle.Allow(key) {
		t.Fatal("blocked beyond cap")
	}
	throttle.RecordFailure(key)
	*now = now.Add(loginBackoffMax)
	if !throttle.Allow(key) {
		t.Fatal("backoff beyond cap still blocked")
	}
}

func TestLoginThrottleKeysIndependentAndSuccessResets(t *testing.T) {
	throttle, now := testThrottle()
	throttle.Allow("user:bob")
	throttle.RecordFailure("user:bob")
	*now = now.Add(loginBackoffBase - time.Second)
	if throttle.Allow("user:bob") {
		t.Fatal("bob not in backoff")
	}
	if !throttle.Allow("ip:192.0.2.8") {
		t.Fatal("unrelated ip key blocked")
	}
	if !throttle.Allow("user:carol") {
		t.Fatal("unrelated user key blocked")
	}
	throttle.RecordSuccess("user:bob")
	if !throttle.Allow("user:bob") {
		t.Fatal("success did not reset guard")
	}
}

func TestLoginThrottleCapacityBoundedAndEvicts(t *testing.T) {
	throttle, now := testThrottle()
	total := loginGuardMaxEntries + 512
	for i := 0; i < total; i++ {
		key := "user:flood-" + strconv.Itoa(i)
		if !throttle.Allow(key) {
			t.Fatalf("key %d blocked", i)
		}
	}
	if len(throttle.entries) > loginGuardMaxEntries {
		t.Fatalf("entries=%d exceed hard cap", len(throttle.entries))
	}

	for i := 0; i < total; i++ {
		key := "user:flood-" + strconv.Itoa(i)
		throttle.guard(key, *now)
	}
	if len(throttle.entries) > loginGuardMaxEntries {
		t.Fatalf("entries=%d exceed hard cap after re-touch", len(throttle.entries))
	}

	*now = now.Add(loginGuardIdleTTL + time.Minute)
	for i := 0; i < loginGuardMaxEntries; i++ {
		throttle.Allow("user:second-wave-" + strconv.Itoa(i))
	}
	if len(throttle.entries) > loginGuardMaxEntries {
		t.Fatalf("entries=%d exceed hard cap after idle expiry", len(throttle.entries))
	}
	idle := 0
	for _, element := range throttle.entries {
		if now.Sub(element.Value.(*loginGuard).lastSeen) >= loginGuardIdleTTL {
			idle++
		}
	}
	if idle != 0 {
		t.Fatalf("idle entries linger: %d", idle)
	}
}
