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
