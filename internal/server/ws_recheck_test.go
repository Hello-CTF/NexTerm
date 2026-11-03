package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type recheckClock struct{ offset atomic.Int64 }

func (c *recheckClock) now() time.Time {
	return time.Unix(1_700_000_000, 0).Add(time.Duration(c.offset.Load()))
}
func (c *recheckClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

func TestSessionRecheckerCoalescesConcurrentCallersPerSession(t *testing.T) {
	var c sessionRechecker
	clock := &recheckClock{}
	c.now = clock.now
	const sockets = 16
	for i := 0; i < sockets; i++ {
		c.acquire("s1")
	}
	c.acquire("s2")
	var lookups atomic.Int64
	release := make(chan struct{})
	lookup := func(context.Context) (*account.Identity, error) {
		lookups.Add(1)
		<-release
		return &account.Identity{SessionID: "s1"}, nil
	}
	var wg sync.WaitGroup
	results := make(chan error, sockets)
	for i := 0; i < sockets; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.check(context.Background(), "s1", time.Minute, lookup)
			results <- err
		}()
	}
	// Hold the lookup until every caller has joined or finished its wait.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("lookups within one interval = %d, want 1", got)
	}
	// Still inside the interval: served from the cache.
	if _, err := c.check(context.Background(), "s1", time.Minute, lookup); err != nil || lookups.Load() != 1 {
		t.Fatalf("cached check err=%v lookups=%d", err, lookups.Load())
	}
	// A different session does not share the result.
	if _, err := c.check(context.Background(), "s2", time.Minute, lookup); err != nil || lookups.Load() != 2 {
		t.Fatalf("other session err=%v lookups=%d", err, lookups.Load())
	}
	clock.advance(time.Minute + time.Second)
	if _, err := c.check(context.Background(), "s1", time.Minute, lookup); err != nil || lookups.Load() != 3 {
		t.Fatalf("next interval err=%v lookups=%d", err, lookups.Load())
	}
}

func TestSessionRecheckerCachesInvalidButNotTransientErrors(t *testing.T) {
	var c sessionRechecker
	c.now = (&recheckClock{}).now
	c.acquire("s1")
	var lookups atomic.Int64
	transient := errors.New("database busy")
	if _, err := c.check(context.Background(), "s1", time.Minute, func(context.Context) (*account.Identity, error) {
		lookups.Add(1)
		return nil, transient
	}); !errors.Is(err, transient) {
		t.Fatalf("err=%v", err)
	}
	invalid := ipc.NewError(ipc.CodeForbidden, "会话已吊销，请重新登录")
	for i := 0; i < 3; i++ {
		_, err := c.check(context.Background(), "s1", time.Minute, func(context.Context) (*account.Identity, error) {
			lookups.Add(1)
			return nil, invalid
		})
		if !confirmedAccountSessionInvalid(err) {
			t.Fatalf("err=%v", err)
		}
	}
	// One transient lookup (retried immediately) plus one conclusive lookup that the next two calls reuse.
	if got := lookups.Load(); got != 2 {
		t.Fatalf("lookups=%d, want 2", got)
	}
}

func TestSessionRecheckerReleasesEntriesWithoutSockets(t *testing.T) {
	var c sessionRechecker
	c.acquire("s1")
	c.acquire("s1")
	c.release("s1")
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n != 1 {
		t.Fatalf("entries=%d with one socket left", n)
	}
	c.release("s1")
	c.release("s1") // extra release must be harmless
	c.mu.Lock()
	n = len(c.entries)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("entries=%d after last socket", n)
	}
}

func TestAccountSocketsOfOneSessionShareTheRecheck(t *testing.T) {
	env := newRevocationEnv(t)
	var mu sync.Mutex
	calls := map[string]int{}
	real := env.fixture.accounts.CheckSession
	env.fixture.server.sessionCheckFn = func(ctx context.Context, token string) (*account.Identity, error) {
		mu.Lock()
		calls[token]++
		mu.Unlock()
		return real(ctx, token)
	}
	env.fixture.server.accountSocketRevalidateNanos.Store(int64(time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sockets := env.openSockets(t, ctx)

	// events and channel sockets of a session register the same entry.
	env.fixture.server.sessionRecheck.mu.Lock()
	entries := len(env.fixture.server.sessionRecheck.entries)
	env.fixture.server.sessionRecheck.mu.Unlock()
	if entries != len(env.sessions) {
		t.Fatalf("entries=%d, want one per session (%d)", entries, len(env.sessions))
	}

	bob := env.sessions["bob"]
	identity, err := env.fixture.accounts.CheckSession(ctx, bob.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	delete(calls, bob.cookie.Value)
	mu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if env.fixture.server.accountSocketSessionInvalid(ctx, time.Hour, bob.cookie.Value, identity) {
				t.Error("valid session reported invalid")
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	got := calls[bob.cookie.Value]
	mu.Unlock()
	if got != 1 {
		t.Fatalf("lookups for one session within an interval = %d, want 1", got)
	}

	for _, socket := range sockets {
		_ = socket.conn.CloseNow()
	}
	waitFor(t, func() bool {
		env.fixture.server.sessionRecheck.mu.Lock()
		defer env.fixture.server.sessionRecheck.mu.Unlock()
		return len(env.fixture.server.sessionRecheck.entries) == 0
	})
}
