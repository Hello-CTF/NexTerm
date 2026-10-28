package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// OwnerWaitFile is the marker written under the data dir while the owner account is unavailable, so a separate
// `agent status` process can report it.
const OwnerWaitFile = "owner-unavailable.json"

// ownerWait is the temporary state entered when the server answers owner_unavailable: the device is intact,
// its owner account is disabled or reset-required, and the agent retries with capped exponential backoff.
type ownerWait struct {
	active  bool
	since   time.Time
	backoff time.Duration
	nextTry time.Time
}

type ownerWaitMarker struct {
	Since time.Time `json:"since"`
}

func (r *Runtime) sleepFor(ctx context.Context, d time.Duration) bool {
	if r.sleep != nil {
		return r.sleep(ctx, d)
	}
	return sleepContext(ctx, d)
}

// handleDenied reacts to an auth denial on a non-reconnecting path (sync, URL report, bridge dial) and reports
// whether err was one: a real revocation stops the agent, owner_unavailable enters the waiting state.
func (r *Runtime) handleDenied(err error) bool {
	if isOwnerUnavailable(err) {
		r.enterOwnerWait()
		return true
	}
	if isForbidden(err) {
		r.revoke()
		return true
	}
	return false
}

// enterOwnerWait records the state, advances the backoff and returns the jittered delay before the next attempt.
func (r *Runtime) enterOwnerWait() time.Duration {
	r.mu.Lock()
	first := !r.owner.active
	if first {
		r.owner = ownerWait{active: true, since: r.now(), backoff: ownerMinBackoff}
	}
	delay := r.jittered(r.owner.backoff)
	r.owner.nextTry = r.now().Add(delay)
	if next := r.owner.backoff * 2; next > ownerMaxBackoff {
		r.owner.backoff = ownerMaxBackoff
	} else {
		r.owner.backoff = next
	}
	since := r.owner.since
	r.mu.Unlock()
	if first {
		r.logger.Warn("device owner account is disabled or requires a password reset; agent keeps running and will reconnect automatically once it is usable again")
		r.writeOwnerMarker(since)
	}
	return delay
}

// waitOwner enters/extends the waiting state and sleeps for the backoff; false means the context ended.
func (r *Runtime) waitOwner(ctx context.Context) bool {
	return r.sleepFor(ctx, r.enterOwnerWait())
}

// ownerWaiting reports whether the next owner retry is not due yet, so periodic work can skip contacting the server.
func (r *Runtime) ownerWaiting() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owner.active && r.now().Before(r.owner.nextTry)
}

// ownerRecovered clears the waiting state after the server accepted the device again.
func (r *Runtime) ownerRecovered() {
	r.mu.Lock()
	was := r.owner.active
	r.owner = ownerWait{}
	r.mu.Unlock()
	if was {
		r.logger.Info("device owner account is usable again; agent resumed")
		r.clearOwnerMarker()
	}
}

func (r *Runtime) ownerMarkerPath() string {
	if r.dataDir == "" {
		return ""
	}
	return filepath.Join(r.dataDir, OwnerWaitFile)
}

func (r *Runtime) writeOwnerMarker(since time.Time) {
	path := r.ownerMarkerPath()
	if path == "" {
		return
	}
	payload, err := json.Marshal(ownerWaitMarker{Since: since})
	if err != nil {
		return
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		r.logger.Warn("persist owner-unavailable state failed", "error", err)
	}
}

func (r *Runtime) clearOwnerMarker() {
	if path := r.ownerMarkerPath(); path != "" {
		_ = os.Remove(path)
	}
}

// ReadOwnerWait returns when the agent entered the owner-unavailable wait, if it currently is in it.
func ReadOwnerWait(dataDir string) (time.Time, bool) {
	payload, err := os.ReadFile(filepath.Join(dataDir, OwnerWaitFile))
	if err != nil {
		return time.Time{}, false
	}
	var marker ownerWaitMarker
	if json.Unmarshal(payload, &marker) != nil {
		return time.Time{}, false
	}
	return marker.Since, true
}
