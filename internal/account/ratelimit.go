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

type loginGuard struct {
	key              string
	windowStart      time.Time
	windowCount      int
	consecutiveFails int
	blockedUntil     time.Time
	lastSeen         time.Time
}

type LoginThrottle struct {
	mu        sync.Mutex
	entries   map[string]*list.Element
	lru       list.List
	now       func() time.Time
	lastSweep time.Time
}

func NewLoginThrottle() *LoginThrottle {
	return &LoginThrottle{entries: make(map[string]*list.Element), now: time.Now}
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
		return guard
	}
	if len(t.entries) >= loginGuardMaxEntries {
		t.evictOne()
	}
	guard := &loginGuard{key: key, windowStart: now, lastSeen: now}
	t.entries[key] = t.lru.PushFront(guard)
	return guard
}

func (t *LoginThrottle) sweep(now time.Time) {
	if len(t.entries) <= loginGuardMaxEntries/2 || now.Sub(t.lastSweep) < loginGuardSweepInterval {
		return
	}
	t.lastSweep = now
	for checked, element := 0, t.lru.Back(); element != nil && checked < loginGuardSweepBatch; checked++ {
		previous := element.Prev()
		if now.Sub(element.Value.(*loginGuard).lastSeen) >= loginGuardIdleTTL {
			t.removeElement(element)
		} else {
			return
		}
		element = previous
	}
}

func (t *LoginThrottle) evictOne() {
	if back := t.lru.Back(); back != nil {
		t.removeElement(back)
	}
}

func (t *LoginThrottle) removeElement(element *list.Element) {
	delete(t.entries, element.Value.(*loginGuard).key)
	t.lru.Remove(element)
}
