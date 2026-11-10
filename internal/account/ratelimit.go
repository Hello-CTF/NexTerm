package account

import (
	"container/list"
	"hash/fnv"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	loginWindow             = time.Minute
	loginWindowLimit        = 5
	loginBackoffBase        = 30 * time.Second
	loginBackoffMax         = time.Hour
	loginGuardIdleTTL       = 10 * time.Minute
	loginGuardMaxEntries    = 65536
	loginGuardSweepInterval = time.Minute
	loginGuardSweepBatch    = 256
	loginOverflowShards     = 256
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
// caller exempts clients holding a device token from an earlier login to the
// account. An attacker can therefore refuse logins from clients without a
// device cookie for as long as the attack lasts; a device with the cookie, or
// a server restart (throttle state is in memory), recovers access.
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
// backoff or an exhausted window are never evicted. When the oldest guards are
// all protected, a key without a guard is limited by an overflow guard instead:
// one of a fixed set of shards chosen by the coarse network of the address the
// key ends with (IPv4 /24, IPv6 /48), each admitting the policy's window and
// never backing off. A flood therefore cannot clear a lockout, cannot refuse
// every new key, and fills only its own networks' shards.
type LoginThrottle struct {
	mu         sync.Mutex
	policy     ThrottlePolicy
	maxEntries int
	entries    map[string]*list.Element
	lru        list.List
	overflow   [loginOverflowShards]loginGuard
	now        func() time.Time
	lastSweep  time.Time
}

func NewLoginThrottle() *LoginThrottle {
	return NewLoginThrottleWithPolicy(DefaultThrottlePolicy)
}

func NewLoginThrottleWithPolicy(policy ThrottlePolicy) *LoginThrottle {
	return &LoginThrottle{policy: policy, maxEntries: loginGuardMaxEntries, entries: make(map[string]*list.Element), now: time.Now}
}

// SetNowForTest replaces the throttle's clock. It exists for tests only.
func (t *LoginThrottle) SetNowForTest(now func() time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = now
}

// Peek reports whether Allow would admit key, without spending an attempt or
// creating a guard. Callers that check several throttles peek all of them
// before calling Allow, so a throttle that refuses does not let the others
// charge for an attempt that never runs.
func (t *LoginThrottle) Peek(key string) bool {
	return t.peek(key, false)
}

// PeekOwn is Peek for a client the caller already trusts (it holds a device
// token for the account): only a guard of its own can refuse it, never a
// shared overflow guard.
func (t *LoginThrottle) PeekOwn(key string) bool {
	return t.peek(key, true)
}

// Allow admits one attempt for key and spends it from the key's window.
func (t *LoginThrottle) Allow(key string) bool {
	return t.allow(key, false)
}

// AllowOwn is Allow for a trusted client; see PeekOwn.
func (t *LoginThrottle) AllowOwn(key string) bool {
	return t.allow(key, true)
}

func (t *LoginThrottle) peek(key string, own bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	guard, overflow := t.lookup(key, now)
	if guard == nil || overflow && own {
		return true
	}
	return t.admits(guard, now)
}

func (t *LoginThrottle) allow(key string, own bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	guard, overflow := t.lookup(key, now)
	if overflow && own {
		return true
	}
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

// RecordFailure extends the key's failure streak. A key limited by an
// overflow guard records nothing: overflow guards only count attempts, so
// failures cannot hold a shard shut.
func (t *LoginThrottle) RecordFailure(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	guard, overflow := t.lookup(key, now)
	if overflow {
		return
	}
	if guard == nil {
		if guard = t.create(key, now); guard.key != key {
			return
		}
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

// lookup returns the key's guard; for a key without one, the overflow shard
// (overflow true) when the table cannot take a guard, or nil when it can.
func (t *LoginThrottle) lookup(key string, now time.Time) (*loginGuard, bool) {
	t.sweep(now)
	if element, ok := t.entries[key]; ok {
		t.lru.MoveToFront(element)
		guard := element.Value.(*loginGuard)
		guard.lastSeen = now
		t.decay(guard, now)
		return guard, false
	}
	if len(t.entries) >= t.maxEntries && t.evictCandidate(now) == nil {
		return &t.overflow[overflowShard(key)], true
	}
	return nil, false
}

// create adds a guard for key, evicting the least recently used unprotected
// guard when the table is full; it returns the overflow shard when it cannot.
func (t *LoginThrottle) create(key string, now time.Time) *loginGuard {
	if len(t.entries) >= t.maxEntries {
		candidate := t.evictCandidate(now)
		if candidate == nil {
			return &t.overflow[overflowShard(key)]
		}
		t.removeElement(candidate)
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

// evictCandidate returns the least recently used unprotected guard among the
// oldest loginGuardSweepBatch ones, so a full table costs a bounded scan. When
// none qualifies the caller falls back to an overflow shard.
func (t *LoginThrottle) evictCandidate(now time.Time) *list.Element {
	checked := 0
	for element := t.lru.Back(); element != nil && checked < loginGuardSweepBatch; element, checked = element.Prev(), checked+1 {
		if !t.protected(element.Value.(*loginGuard), now) {
			return element
		}
	}
	return nil
}

func (t *LoginThrottle) removeElement(element *list.Element) {
	delete(t.entries, element.Value.(*loginGuard).key)
	t.lru.Remove(element)
}

// overflowShard picks the overflow shard of a key by the coarse network of the
// address after its last "|" (IPv4 /24, IPv6 /48; an IPv6 /64 prefix as built
// by the server's throttle keys is coarsened the same way). Keys without an
// address hash as a whole.
func overflowShard(key string) int {
	network := key
	if index := strings.LastIndexByte(key, '|'); index >= 0 {
		network = key[index+1:]
	}
	if prefix, err := netip.ParsePrefix(network); err == nil {
		network = coarseNetwork(prefix.Addr())
	} else if addr, err := netip.ParseAddr(network); err == nil {
		network = coarseNetwork(addr)
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(network))
	return int(hash.Sum32() % loginOverflowShards)
}

func coarseNetwork(addr netip.Addr) string {
	addr = addr.Unmap().WithZone("")
	bits := 48
	if addr.Is4() {
		bits = 24
	}
	prefix, err := addr.Prefix(bits)
	if err != nil {
		return addr.String()
	}
	return prefix.String()
}
