package account

import (
	"container/list"
	"sync"
	"time"
)

const (
	loginWindow             = time.Minute
	loginWindowLimit        = 5
	loginBackoffBase        = 30 * time.Second
	loginBackoffMax         = time.Hour
	loginGuardIdleTTL       = 10 * time.Minute
	loginGuardMaxEntries    = 4096
	loginGuardSweepInterval = time.Minute
	loginGuardSweepBatch    = 256
)

// ThrottlePolicy configures a LoginThrottle.
type ThrottlePolicy struct {
	// WindowLimit attempts are admitted per Window.
	Window      time.Duration
	WindowLimit int
	// FreeFailures consecutive failures are tolerated before backoff starts;
	// each further failure doubles the backoff from BackoffBase up to BackoffMax.
	FreeFailures int
	BackoffBase  time.Duration
	BackoffMax   time.Duration
	// FailureDecay, when nonzero, forgets the failure streak after that long
	// without a new failure, so a shared bucket does not escalate forever.
	FailureDecay time.Duration
}

// DefaultThrottlePolicy is the per-credential policy: five attempts a minute
// and a doubling backoff from the first failure.
var DefaultThrottlePolicy = ThrottlePolicy{
	Window: loginWindow, WindowLimit: loginWindowLimit,
	BackoffBase: loginBackoffBase, BackoffMax: loginBackoffMax,
}

// LoginIPThrottlePolicy bounds password attempts from one client address
// across all usernames. It admits more than the per-credential bucket so
// users behind one NAT can still log in, and its streak decays.
var LoginIPThrottlePolicy = ThrottlePolicy{
	Window: loginWindow, WindowLimit: 60,
	FreeFailures: 30, BackoffBase: loginBackoffBase, BackoffMax: 15 * time.Minute,
	FailureDecay: loginGuardIdleTTL,
}

// LoginUserThrottlePolicy bounds password attempts against one username from
// all addresses. Its backoff is capped short so an attacker can delay but not
// lock out the real user.
var LoginUserThrottlePolicy = ThrottlePolicy{
	Window: loginWindow, WindowLimit: 30,
	FreeFailures: 10, BackoffBase: loginBackoffBase, BackoffMax: 5 * time.Minute,
	FailureDecay: loginGuardIdleTTL,
}

type loginGuard struct {
	key              string
	windowStart      time.Time
	windowCount      int
	consecutiveFails int
	lastFailure      time.Time
	blockedUntil     time.Time
	lastSeen         time.Time
}

type LoginThrottle struct {
	mu         sync.Mutex
	policy     ThrottlePolicy
	maxEntries int
	entries    map[string]*list.Element
	lru        list.List
	now        func() time.Time
	lastSweep  time.Time
}

func NewLoginThrottle() *LoginThrottle {
	return NewLoginThrottleWithPolicy(DefaultThrottlePolicy)
}

func NewLoginThrottleWithPolicy(policy ThrottlePolicy) *LoginThrottle {
	return &LoginThrottle{policy: policy, maxEntries: loginGuardMaxEntries, entries: make(map[string]*list.Element), now: time.Now}
}

// Allow admits one attempt for key. When the guard table is full of keys that
// are serving a backoff or an exhausted window, a new key is refused rather
// than evicting one of them.
func (t *LoginThrottle) Allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	guard := t.guard(key, now)
	if guard == nil {
		return false
	}
	if now.Before(guard.blockedUntil) {
		return false
	}
	if now.Sub(guard.windowStart) >= t.policy.Window {
		guard.windowStart = now
		guard.windowCount = 0
	}
	if guard.windowCount >= t.policy.WindowLimit {
		return false
	}
	guard.windowCount++
	return true
}

func (t *LoginThrottle) RecordFailure(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	guard := t.guard(key, now)
	if guard == nil {
		return
	}
	guard.consecutiveFails++
	guard.lastFailure = now
	over := guard.consecutiveFails - t.policy.FreeFailures
	if over <= 0 {
		return
	}
	backoff := t.policy.BackoffBase << (over - 1)
	if over > 62 || backoff > t.policy.BackoffMax || backoff <= 0 {
		backoff = t.policy.BackoffMax
	}
	guard.blockedUntil = now.Add(backoff)
}

func (t *LoginThrottle) RecordSuccess(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if element, ok := t.entries[key]; ok {
		t.removeElement(element)
	}
}

func (t *LoginThrottle) guard(key string, now time.Time) *loginGuard {
	t.sweep(now)
	if element, ok := t.entries[key]; ok {
		t.lru.MoveToFront(element)
		guard := element.Value.(*loginGuard)
		guard.lastSeen = now
		if t.policy.FailureDecay > 0 && guard.consecutiveFails > 0 && !now.Before(guard.blockedUntil) &&
			now.Sub(guard.lastFailure) >= t.policy.FailureDecay {
			guard.consecutiveFails = 0
		}
		return guard
	}
	if len(t.entries) >= t.maxEntries && !t.evictOne(now) {
		return nil
	}
	guard := &loginGuard{key: key, windowStart: now, lastSeen: now}
	t.entries[key] = t.lru.PushFront(guard)
	return guard
}

// protected reports whether dropping the guard would hand its key fresh
// attempts: it is serving a backoff or has exhausted its current window.
func (t *LoginThrottle) protected(guard *loginGuard, now time.Time) bool {
	if now.Before(guard.blockedUntil) {
		return true
	}
	return guard.windowCount >= t.policy.WindowLimit && now.Sub(guard.windowStart) < t.policy.Window
}

func (t *LoginThrottle) sweep(now time.Time) {
	if len(t.entries) <= t.maxEntries/2 || now.Sub(t.lastSweep) < loginGuardSweepInterval {
		return
	}
	t.lastSweep = now
	for checked, element := 0, t.lru.Back(); element != nil && checked < loginGuardSweepBatch; checked++ {
		previous := element.Prev()
		guard := element.Value.(*loginGuard)
		if now.Sub(guard.lastSeen) < loginGuardIdleTTL {
			return
		}
		if !t.protected(guard, now) {
			t.removeElement(element)
		}
		element = previous
	}
}

// evictOne drops the least recently used guard that is not protected, so
// flooding the table with unrelated keys cannot clear an active backoff. It
// reports false when every guard is protected.
func (t *LoginThrottle) evictOne(now time.Time) bool {
	for element := t.lru.Back(); element != nil; element = element.Prev() {
		if !t.protected(element.Value.(*loginGuard), now) {
			t.removeElement(element)
			return true
		}
	}
	return false
}

func (t *LoginThrottle) removeElement(element *list.Element) {
	delete(t.entries, element.Value.(*loginGuard).key)
	t.lru.Remove(element)
}
