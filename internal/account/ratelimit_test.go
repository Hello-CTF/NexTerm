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
		throttle.Allow(key)
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

func testPolicyThrottle(policy ThrottlePolicy) (*LoginThrottle, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	throttle := NewLoginThrottleWithPolicy(policy)
	throttle.now = func() time.Time { return now }
	return throttle, &now
}

// The per-username bucket counts failures only, tolerates a streak before
// backing off, caps the backoff short, and forgets the streak after a quiet
// period.
func TestLoginUserThrottlePolicyBacksOffWithShortCap(t *testing.T) {
	policy := LoginUserThrottlePolicy
	throttle, now := testPolicyThrottle(policy)
	key := "login-user|alice"
	for i := 0; i < 1000; i++ {
		if !throttle.Allow(key) {
			t.Fatalf("attempt %d without failures refused", i+1)
		}
	}
	if len(throttle.entries) != 0 {
		t.Fatalf("attempts without failures created %d guards", len(throttle.entries))
	}
	for i := 0; i < policy.FreeFailures; i++ {
		if !throttle.Allow(key) {
			t.Fatalf("attempt %d refused within the free streak", i+1)
		}
		throttle.RecordFailure(key)
	}
	if !throttle.Allow(key) {
		t.Fatal("attempt refused right after the free streak")
	}
	throttle.RecordFailure(key)
	if throttle.Allow(key) {
		t.Fatal("failure past the free streak did not back off")
	}
	for i := 0; i < 20; i++ {
		*now = now.Add(policy.BackoffMax)
		if !throttle.Allow(key) {
			t.Fatalf("round %d still refused after the backoff cap", i)
		}
		throttle.RecordFailure(key)
	}
	*now = now.Add(policy.BackoffMax + policy.FailureDecay)
	if !throttle.Allow(key) {
		t.Fatal("attempt refused after the streak decayed")
	}
	throttle.RecordFailure(key)
	if !throttle.Allow(key) {
		t.Fatal("decayed streak still backs off on the first new failure")
	}
}

func TestKnownClientsExpireAndEvictOldest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	known := NewKnownClients(2, time.Hour)
	known.now = func() time.Time { return now }
	known.Add("a")
	known.Add("b")
	if !known.Contains("a") || !known.Contains("b") || known.Contains("c") {
		t.Fatal("known set lookup wrong")
	}
	known.Add("c")
	if known.Contains("a") {
		t.Fatal("oldest key kept beyond capacity")
	}
	now = now.Add(time.Hour)
	if known.Contains("b") || known.Contains("c") {
		t.Fatal("expired key still known")
	}
}

func TestLoginIPThrottlePolicyAdmitsMoreThanPerCredential(t *testing.T) {
	policy := LoginIPThrottlePolicy
	throttle, now := testPolicyThrottle(policy)
	key := "login-ip|192.0.2.7"
	for i := 0; i < policy.FreeFailures; i++ {
		if !throttle.Allow(key) {
			t.Fatalf("attempt %d refused within the free streak", i+1)
		}
		throttle.RecordFailure(key)
		*now = now.Add(2 * time.Second)
	}
	if !throttle.Allow(key) {
		t.Fatal("attempt refused right after the free streak")
	}
	throttle.RecordFailure(key)
	if throttle.Allow(key) {
		t.Fatal("address past the free streak did not back off")
	}
	*now = now.Add(policy.BackoffBase)
	if !throttle.Allow(key) {
		t.Fatal("address still refused after the first backoff")
	}
}
