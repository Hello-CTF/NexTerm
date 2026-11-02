package ipc

import "sync"

type userRevocation struct {
	epoch  uint64
	except string
}

// UserRevocations records user-level revocations (password change, reset, disable) so an operation that passed its
// session check before the revocation cannot register a connection after it. Memory is one small entry per user
// that was ever revoked.
type UserRevocations struct {
	mu      sync.Mutex
	counter uint64
	users   map[string]userRevocation
}

// Epoch is read before an operation starts its session check.
func (r *UserRevocations) Epoch(userID string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.users[userID].epoch
}

// Revoke records a revocation of userID that spares exceptSession (empty spares nothing).
func (r *UserRevocations) Revoke(userID, exceptSession string) {
	if userID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.users == nil {
		r.users = map[string]userRevocation{}
	}
	r.counter++
	r.users[userID] = userRevocation{epoch: r.counter, except: exceptSession}
}

// Stale reports whether a revocation of userID happened after epoch that did not spare loginSession.
func (r *UserRevocations) Stale(userID string, epoch uint64, loginSession string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.users[userID]
	if !ok || rec.epoch <= epoch {
		return false
	}
	return rec.except == "" || rec.except != loginSession
}

// MatchesRevoked reports whether a connection of loginSession is hit by a revocation sparing exceptSession.
func MatchesRevoked(loginSession, exceptSession string) bool {
	return exceptSession == "" || loginSession != exceptSession
}
