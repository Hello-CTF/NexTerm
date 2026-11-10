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
	// WindowLimit attempts are admitted per Window; zero disables the window,
	// so only failures (through the backoff) limit the key.
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

// LoginUserThrottlePolicy bounds failed password attempts against one
// username from all addresses. It counts failures only, so refused or
// successful attempts never spend it, and its backoff is capped short. The
// caller exempts clients that already logged in to the account, so an
// attacker can delay logins from new addresses but not lock the user out.
var LoginUserThrottlePolicy = ThrottlePolicy{
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

// LoginThrottle tracks attempts per key in a bounded table. Guards serving a
// backoff or an exhausted window are never evicted; when the table holds only
// such guards, keys without a guard share one overflow guard under the same
// policy, so a flood cannot clear a lockout and cannot refuse every new key.
type LoginThrottle struct {
	mu         sync.Mutex
	policy     ThrottlePolicy
	maxEntries int
	entries    map[string]*list.Element
	lru        list.List
	overflow   loginGuard
	now        func() time.Time
	lastSweep  time.Time
}

func NewLoginThrottle() *LoginThrottle {
	return NewLoginThrottleWithPolicy(DefaultThrottlePolicy)
}

func NewLoginThrottleWithPolicy(policy ThrottlePolicy) *LoginThrottle {
	return &LoginThrottle{policy: policy, maxEntries: loginGuardMaxEntries, entries: make(map[string]*list.Element), now: time.Now}
}

// Peek reports whether Allow would admit key, without spending an attempt or
// creating a guard. Callers that check several throttles peek all of them
// before calling Allow, so a throttle that refuses does not let the others
// charge for an attempt that never runs.
func (t *LoginThrottle) Peek(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	guard := t.lookup(key, now)
	if guard == nil {
		return true
	}
	return t.admits(guard, now)
}

// Allow admits one attempt for key and spends it from the key's window.
func (t *LoginThrottle) Allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	guard := t.lookup(key, now)
	if guard == nil {
		if t.policy.WindowLimit <= 0 {
			return true
		}
		guard = t.create(key, now)
	}
	if !t.admits(guard, now) {
		return false
	}
	if t.policy.WindowLimit > 0 {
		if now.Sub(guard.windowStart) >= t.policy.Window {
			guard.windowStart = now
			guard.windowCount = 0
		}
		guard.windowCount++
	}
	return true
}

func (t *LoginThrottle) RecordFailure(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	guard := t.lookup(key, now)
	if guard == nil {
		guard = t.create(key, now)
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

func (t *LoginThrottle) admits(guard *loginGuard, now time.Time) bool {
	if now.Before(guard.blockedUntil) {
		return false
	}
	if t.policy.WindowLimit <= 0 {
		return true
	}
	return now.Sub(guard.windowStart) >= t.policy.Window || guard.windowCount < t.policy.WindowLimit
}

// lookup returns the key's guard, the overflow guard when the key has none
// and the table cannot take one, or nil when a new guard can be created.
func (t *LoginThrottle) lookup(key string, now time.Time) *loginGuard {
	t.sweep(now)
	if element, ok := t.entries[key]; ok {
		t.lru.MoveToFront(element)
		guard := element.Value.(*loginGuard)
		guard.lastSeen = now
		t.decay(guard, now)
		return guard
	}
	if len(t.entries) >= t.maxEntries && !t.evictable(now) {
		t.decay(&t.overflow, now)
		return &t.overflow
	}
	return nil
}

// create adds a guard for key, evicting the least recently used unprotected
// guard when the table is full. It falls back to the overflow guard when every
// guard is protected.
func (t *LoginThrottle) create(key string, now time.Time) *loginGuard {
	if len(t.entries) >= t.maxEntries && !t.evictOne(now) {
		return &t.overflow
	}
	guard := &loginGuard{key: key, windowStart: now, lastSeen: now}
	t.entries[key] = t.lru.PushFront(guard)
	return guard
}

func (t *LoginThrottle) decay(guard *loginGuard, now time.Time) {
	if t.policy.FailureDecay > 0 && guard.consecutiveFails > 0 && !now.Before(guard.blockedUntil) &&
		now.Sub(guard.lastFailure) >= t.policy.FailureDecay {
		guard.consecutiveFails = 0
	}
}

// protected reports whether dropping the guard would hand its key fresh
// attempts: it is serving a backoff or has exhausted its current window.
func (t *LoginThrottle) protected(guard *loginGuard, now time.Time) bool {
	if now.Before(guard.blockedUntil) {
		return true
	}
	return t.policy.WindowLimit > 0 && guard.windowCount >= t.policy.WindowLimit && now.Sub(guard.windowStart) < t.policy.Window
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

func (t *LoginThrottle) evictable(now time.Time) bool {
	for element := t.lru.Back(); element != nil; element = element.Prev() {
		if !t.protected(element.Value.(*loginGuard), now) {
			return true
		}
	}
	return false
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

// KnownClients remembers keys (such as a username with a client address) that
// completed a login recently, so throttles aimed at strangers can exempt them.
// Only a successful login adds a key, so an attacker cannot fill it without
// valid credentials; the oldest key goes when it is full.
type KnownClients struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]*list.Element
	lru     list.List
	now     func() time.Time
}

type knownClient struct {
	key  string
	seen time.Time
}

func NewKnownClients(max int, ttl time.Duration) *KnownClients {
	return &KnownClients{ttl: ttl, max: max, entries: make(map[string]*list.Element), now: time.Now}
}

func (k *KnownClients) Add(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	if element, ok := k.entries[key]; ok {
		element.Value.(*knownClient).seen = now
		k.lru.MoveToFront(element)
		return
	}
	if len(k.entries) >= k.max {
		if back := k.lru.Back(); back != nil {
			delete(k.entries, back.Value.(*knownClient).key)
			k.lru.Remove(back)
		}
	}
	k.entries[key] = k.lru.PushFront(&knownClient{key: key, seen: now})
}

func (k *KnownClients) Contains(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	element, ok := k.entries[key]
	if !ok {
		return false
	}
	if k.now().Sub(element.Value.(*knownClient).seen) >= k.ttl {
		delete(k.entries, key)
		k.lru.Remove(element)
		return false
	}
	return true
}
