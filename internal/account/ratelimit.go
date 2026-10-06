package account

import (
	"sync"
	"time"
)

const (
	loginWindow          = time.Minute
	loginWindowLimit     = 5
	loginBackoffBase     = 30 * time.Second
	loginBackoffMax      = time.Hour
	loginGuardIdleTTL    = 10 * time.Minute
	loginGuardSweepLimit = 1024
)

type loginGuard struct {
	windowStart      time.Time
	windowCount      int
	consecutiveFails int
	blockedUntil     time.Time
	lastSeen         time.Time
}

type LoginThrottle struct {
	mu      sync.Mutex
	entries map[string]*loginGuard
	now     func() time.Time
}

func NewLoginThrottle() *LoginThrottle {
	return &LoginThrottle{entries: make(map[string]*loginGuard), now: time.Now}
}

func (t *LoginThrottle) Allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	guard := t.guard(key, now)
	if now.Before(guard.blockedUntil) {
		return false
	}
	if now.Sub(guard.windowStart) >= loginWindow {
		guard.windowStart = now
		guard.windowCount = 0
	}
	if guard.windowCount >= loginWindowLimit {
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
	guard.consecutiveFails++
	backoff := loginBackoffBase << (guard.consecutiveFails - 1)
	if backoff > loginBackoffMax || backoff <= 0 {
		backoff = loginBackoffMax
	}
	guard.blockedUntil = now.Add(backoff)
}

func (t *LoginThrottle) RecordSuccess(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

func (t *LoginThrottle) guard(key string, now time.Time) *loginGuard {
	if len(t.entries) > loginGuardSweepLimit {
		for existing, entry := range t.entries {
			if now.Sub(entry.lastSeen) >= loginGuardIdleTTL {
				delete(t.entries, existing)
			}
		}
	}
	guard, ok := t.entries[key]
	if !ok {
		guard = &loginGuard{windowStart: now}
		t.entries[key] = guard
	}
	guard.lastSeen = now
	return guard
}
