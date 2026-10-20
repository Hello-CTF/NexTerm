package account

import "sync"

// epochTracker tracks a per-account monotonic generation ("epoch").
//
// Session revocation, password changes, recovery and admin resets, device
// revocations, and disabling an account bump the account's epoch. Async
// boundaries (login issuance, DEK envelope reads and uploads, MFA tickets and
// TOTP binding) capture the epoch when work is admitted and re-check it before
// committing effects, so work admitted under an older generation fails closed
// instead of committing across a revocation or reset.
//
// The tracker is intentionally in-memory: the races it closes are in-flight
// request windows inside one server process. A restart resets the generation;
// stored state (sessions, envelopes) remains the source of truth.
type epochTracker struct {
	mu      sync.Mutex
	values  map[string]uint64
	pending map[string]uint64
}

func newEpochTracker() *epochTracker {
	return &epochTracker{values: make(map[string]uint64), pending: make(map[string]uint64)}
}

func (t *epochTracker) current(userID string) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.values[userID]
}

func (t *epochTracker) bump(userID string) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.values[userID]++
	return t.values[userID]
}

func (t *epochTracker) check(userID string, epoch uint64) bool {
	return t.current(userID) == epoch
}

// notePending binds a multi-step flow (a pending TOTP binding) to the account's
// current generation; checkPending re-checks the binding at the confirm step.
func (t *epochTracker) notePending(userID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending[userID] = t.values[userID]
}

func (t *epochTracker) checkPending(userID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	epoch, ok := t.pending[userID]
	return ok && epoch == t.values[userID]
}

func (t *epochTracker) clearPending(userID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, userID)
}

// Epoch returns the account's current async-boundary generation.
func (a *Accounts) Epoch(userID string) uint64 {
	return a.epochs.current(userID)
}

// CheckEpoch reports whether the account is still at the captured generation.
func (a *Accounts) CheckEpoch(userID string, epoch uint64) bool {
	return a.epochs.check(userID, epoch)
}

// BumpEpoch advances the account's async-boundary generation.
func (a *Accounts) BumpEpoch(userID string) uint64 {
	return a.epochs.bump(userID)
}

// RevocationMu returns the shared async-boundary mutex. Server and fleet
// revocation paths serialize on it so an epoch bump cannot land inside another
// boundary's check-to-commit window.
func (a *Accounts) RevocationMu() *sync.Mutex {
	return &a.revocationMu
}
