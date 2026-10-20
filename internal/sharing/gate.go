package sharing

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

var (
	// ErrInputNotAllowed means the input direction of a read-only grant is rejected.
	ErrInputNotAllowed = errors.New("sharing: grant is read-only")
	// ErrGrantExpired means the grant is past its validity period; both directions must stop.
	ErrGrantExpired = errors.New("sharing: grant expired")
)

// RecheckFunc lets the execution path refresh the authorization mid-session against the current database state (e.g. calling
// Service.RevalidateLink / Service.RevalidateHostAccess); the returned Grant replaces the
// snapshot and takes effect on subsequent decisions immediately (permission shrinkage applies at once); an error stops both directions.
type RecheckFunc func(ctx context.Context, grant *Grant) (*Grant, error)

// Gate is the enforcement point of a share stream: the output direction (terminal to viewer) is always allowed, the input direction only for
// read_write grants; each data block is rechecked once before Read and once before Write (use
// WithRevalidateTTL to converge revalidation into a short TTL cache); once the grant becomes invalid both directions stop immediately,
// and blocks read during invalidity or cancellation are discarded. A blocked Read is woken by the integration layer closing the source on cancellation.
type Gate struct {
	refreshMu  refreshLock
	mu         sync.Mutex
	grant      *Grant
	now        func() int64
	recheck    RecheckFunc
	inputCheck func(ctx context.Context, grant *Grant) (*Grant, error)

	revalidateTTL time.Duration
	recheckCache  refreshCache
	inputCache    refreshCache
}

// refreshLock serializes grant refreshes. It is an instance field (never a
// package global) so tests can instrument attempt ordering.
type refreshLock interface {
	Lock()
	Unlock()
}

type mutexRefreshLock struct{ mu sync.Mutex }

func (m *mutexRefreshLock) Lock()   { m.mu.Lock() }
func (m *mutexRefreshLock) Unlock() { m.mu.Unlock() }

// refreshCache is an entry of the revalidate short-TTL cache: grant is the snapshot from the last successful refresh,
// and at is its refresh time (milliseconds).
type refreshCache struct {
	grant *Grant
	at    int64
}

type GateOption func(*Gate)

func WithGateNow(now func() int64) GateOption {
	return func(g *Gate) {
		if now != nil {
			g.now = now
		}
	}
}

func WithRecheck(recheck RecheckFunc) GateOption {
	return func(g *Gate) {
		g.recheck = recheck
	}
}

// WithInputCheck attaches a per-block enforcement point before Write for the input direction (e.g. the audited input
// check of public links); the returned Grant likewise replaces the snapshot.
func WithInputCheck(check func(ctx context.Context, grant *Grant) (*Grant, error)) GateOption {
	return func(g *Gate) {
		g.inputCheck = check
	}
}

// WithRevalidateTTL turns on the short TTL cache for recheck and inputCheck: repeated calls within the TTL are not
// revalidated and reuse the last refresh snapshot; revocation/shrinkage takes effect at the latest on the next call after the TTL
// (idle connections are covered by the integration layer's periodic Check). A non-positive value (the default) disables the cache and revalidates per block.
func WithRevalidateTTL(ttl time.Duration) GateOption {
	return func(g *Gate) {
		g.revalidateTTL = ttl
	}
}

func NewGate(grant *Grant, options ...GateOption) *Gate {
	g := &Gate{grant: grant, now: ids.NowMS, refreshMu: &mutexRefreshLock{}}
	for _, option := range options {
		option(g)
	}
	return g
}

func (g *Gate) Grant() *Grant {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.grant == nil {
		return nil
	}
	copied := *g.grant
	return &copied
}

func (g *Gate) setGrant(grant *Grant) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.grant = grant
}

func (g *Gate) InputAllowed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.grant != nil && g.grant.Permission.AllowsWrite()
}

// Check first refreshes the grant snapshot via recheck (covering permission shrinkage and expiry changes), then judges by the current
// ExpiresAt; expiry or a failed recheck (revocation) both return an error.
func (g *Gate) Check(ctx context.Context) error {
	return g.check(ctx, func(ctx context.Context, grant *Grant) (*Grant, error) {
		return g.cachedRefresh(ctx, grant, g.recheck, &g.recheckCache)
	})
}

// CheckFresh decides the same as Check but bypasses the TTL cache to force a revalidation: the integration layer uses it to
// fetch the current authorization state once the data path is already torn down, so the cache window cannot hide an expiry/revocation that just happened.
func (g *Gate) CheckFresh(ctx context.Context) error {
	return g.check(ctx, func(ctx context.Context, grant *Grant) (*Grant, error) {
		if g.recheck == nil {
			return nil, nil
		}
		return g.recheck(ctx, grant)
	})
}

func (g *Gate) check(ctx context.Context, refresh func(ctx context.Context, grant *Grant) (*Grant, error)) error {
	g.refreshMu.Lock()
	defer g.refreshMu.Unlock()
	grant := g.Grant()
	if grant == nil {
		return errors.New("sharing: nil grant")
	}
	refreshed, err := refresh(ctx, grant)
	if err != nil {
		return err
	}
	if refreshed != nil {
		g.setGrant(refreshed)
	}
	if g.now() >= g.Grant().ExpiresAt {
		return ErrGrantExpired
	}
	return nil
}

// cachedRefresh performs one revalidation: within the TTL window it returns nil directly (keeping the current snapshot;
// the cache entry is always older than or equal to the current snapshot, so it can never roll permissions back to a wider state); outside the window it makes the real
// call, and a successful refresh is written to the cache. Errors are not cached; revocation passes through immediately.
func (g *Gate) cachedRefresh(ctx context.Context, grant *Grant, check RecheckFunc, cache *refreshCache) (*Grant, error) {
	if check == nil {
		return nil, nil
	}
	g.mu.Lock()
	ttl, last := g.revalidateTTL, *cache
	g.mu.Unlock()
	if ttl > 0 && last.grant != nil && g.now()-last.at < ttl.Milliseconds() {
		return nil, nil
	}
	refreshed, err := check(ctx, grant)
	if err != nil {
		return nil, err
	}
	if refreshed != nil && ttl > 0 {
		g.mu.Lock()
		*cache = refreshCache{grant: refreshed, at: g.now()}
		g.mu.Unlock()
	}
	return refreshed, nil
}

// PipeOutput copies terminal output to the viewer until the source ends, the context ends, or the grant becomes invalid.
func (g *Gate) PipeOutput(ctx context.Context, dst io.Writer, src io.Reader) error {
	return g.pipe(ctx, dst, src, false)
}

// PipeInput copies viewer input to the terminal; a read-only grant is rejected and not a single byte is let through.
func (g *Gate) PipeInput(ctx context.Context, dst io.Writer, src io.Reader) error {
	return g.pipe(ctx, dst, src, true)
}

func (g *Gate) refreshInput(ctx context.Context) error {
	g.refreshMu.Lock()
	defer g.refreshMu.Unlock()
	refreshed, err := g.cachedRefresh(ctx, g.Grant(), g.inputCheck, &g.inputCache)
	if err != nil {
		return err
	}
	if refreshed != nil {
		g.setGrant(refreshed)
	}
	return nil
}

func (g *Gate) pipe(ctx context.Context, dst io.Writer, src io.Reader, write bool) error {
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := g.Check(ctx); err != nil {
			return err
		}
		if write && !g.InputAllowed() {
			return ErrInputNotAllowed
		}
		count, readErr := src.Read(buffer)
		if err := ctx.Err(); err != nil {
			return err
		}
		if count > 0 {
			if write && g.inputCheck != nil {
				if err := g.refreshInput(ctx); err != nil {
					return err
				}
			} else {
				if err := g.Check(ctx); err != nil {
					return err
				}
			}
			if write && !g.InputAllowed() {
				return ErrInputNotAllowed
			}
			if _, err := dst.Write(buffer[:count]); err != nil {
				return err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}
