package fleetserver

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// defaultBridgeRevalidateInterval matches the main server's account socket recheck.
const defaultBridgeRevalidateInterval = 30 * time.Second

// bridgeSessionReloginMessage is the close reason of a relay whose login session ended; it matches the account
// socket close reason so clients show the same next step.
const bridgeSessionReloginMessage = "会话无效或缺失，请重新登录"

var errBridgeSessionRevoked = errors.New("login session revoked")

// bridgeSessions tracks the owner relays of /fleet/devices/{id}/bridge per login session, so a revocation
// (logout, logout-all, session revoke, password change) and a periodic session recheck close them.
// One supervisor goroutine runs per login session while it holds at least one relay.
type bridgeSessions struct {
	mu       sync.Mutex
	nextID   uint64
	sessions map[string]*bridgeSession

	interval time.Duration
	ticker   func(time.Duration) (<-chan time.Time, func())
	check    func(context.Context, string) (*account.Identity, error)
}

type bridgeSession struct {
	userID string
	relays map[uint64]context.CancelCauseFunc
	stop   context.CancelFunc
}

func newBridgeSessions(check func(context.Context, string) (*account.Identity, error)) *bridgeSessions {
	return &bridgeSessions{
		sessions: make(map[string]*bridgeSession),
		interval: defaultBridgeRevalidateInterval,
		ticker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
		check: check,
	}
}

// WithBridgeRevalidateInterval overrides the periodic login session recheck of owner bridge relays.
func WithBridgeRevalidateInterval(interval time.Duration) Option {
	return func(s *Service) {
		if interval > 0 {
			s.bridges.interval = interval
		}
	}
}

// track registers a relay of identity's login session. The returned context is cancelled with
// errBridgeSessionRevoked once the session is revoked or found invalid; release unregisters the relay.
func (b *bridgeSessions) track(parent context.Context, identity *account.Identity, token string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	entry := b.sessions[identity.SessionID]
	if entry == nil {
		superviseCtx, stop := context.WithCancel(context.Background())
		entry = &bridgeSession{userID: identity.UserID, relays: make(map[uint64]context.CancelCauseFunc), stop: stop}
		b.sessions[identity.SessionID] = entry
		ticks, stopTicks := b.ticker(b.interval)
		go b.supervise(superviseCtx, ticks, stopTicks, identity, token)
	}
	entry.relays[id] = cancel
	b.mu.Unlock()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel(nil)
			b.mu.Lock()
			delete(entry.relays, id)
			if len(entry.relays) == 0 && b.sessions[identity.SessionID] == entry {
				delete(b.sessions, identity.SessionID)
				entry.stop()
			}
			b.mu.Unlock()
		})
	}
}

// supervise rechecks the login session without sliding it; only a confirmed invalid session closes its relays,
// a transient lookup error keeps them.
func (b *bridgeSessions) supervise(ctx context.Context, ticks <-chan time.Time, stopTicks func(), identity *account.Identity, token string) {
	defer stopTicks()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if b.sessionInvalid(ctx, identity, token) {
				b.revokeSession(identity.SessionID)
				return
			}
		}
	}
}

func (b *bridgeSessions) sessionInvalid(ctx context.Context, identity *account.Identity, token string) bool {
	if b.check == nil || token == "" {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	current, err := b.check(checkCtx, token)
	if err != nil {
		var appErr *ipc.Error
		return errors.As(err, &appErr) && appErr.Code == ipc.CodeForbidden
	}
	return current.SessionID != identity.SessionID || current.UserID != identity.UserID || current.State == account.StateResetRequired
}

func (b *bridgeSessions) revokeSession(sessionID string) {
	b.revokeMatching(func(id string, _ *bridgeSession) bool { return id == sessionID })
}

func (b *bridgeSessions) revokeUser(userID, exceptSession string) {
	b.revokeMatching(func(id string, entry *bridgeSession) bool {
		return entry.userID == userID && ipc.MatchesRevoked(id, exceptSession)
	})
}

// revokeMatching removes the matching sessions from the table, so no new relay joins them, and cancels their relays.
func (b *bridgeSessions) revokeMatching(match func(string, *bridgeSession) bool) {
	b.mu.Lock()
	var cancels []context.CancelCauseFunc
	for sessionID, entry := range b.sessions {
		if !match(sessionID, entry) {
			continue
		}
		delete(b.sessions, sessionID)
		entry.stop()
		for _, cancel := range entry.relays {
			cancels = append(cancels, cancel)
		}
	}
	b.mu.Unlock()
	for _, cancel := range cancels {
		cancel(errBridgeSessionRevoked)
	}
}

// CloseAccountSession closes the owner bridge relays opened under a revoked login session.
func (s *Service) CloseAccountSession(sessionID string) error {
	if sessionID != "" {
		s.bridges.revokeSession(sessionID)
	}
	return nil
}

// CloseAccountUser closes the owner bridge relays of a user's login sessions other than exceptSession, for
// revocations that do not enumerate session ids (password change, recovery reset, disabling the account).
func (s *Service) CloseAccountUser(userID, exceptSession string) {
	if userID != "" {
		s.bridges.revokeUser(userID, exceptSession)
	}
}
