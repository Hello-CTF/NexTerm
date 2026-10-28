package session

import (
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminalgrid"
)

// Multi-client input. Every client of the owning account may write to a tab;
// there is no exclusive controller. Three roles follow from who typed last:
//
//   - the answering client is the most recent input client among the
//     subscribers that answer terminal queries and take output now; its
//     terminal emulator answers queries, every other subscriber is told the
//     range was answered. Without one the headless screen answers;
//   - the size owner is the client whose terminal size the PTY follows.
//     It moves to a client that types once the owner has not typed for
//     sizeHandoffHold, so two clients typing in turn do not resize the PTY
//     on every key. Other clients render the PTY size;
//   - lastInputClient (input_owner.go) is the newest client whose input was
//     written; live clipboard writes apply only on its views.
//
// "Most recently active" ranks attached clients by their latest input and,
// among clients that have not typed, the size owner first, then the latest
// attach. So a single
// client answers and sizes its tab without typing first, and when the
// answering client or the size owner leaves, the most recently active
// remaining client takes over: the headless screen answers only while no
// interactive client is attached. A client that attaches while nobody owns
// the size takes it.
//
// Preview guests write through WriteGuest under their own lease rules: a
// guest may take input control only while no owner client is attached, and
// any owner input takes it back.

// sizeHandoffHold is how long the size owner keeps the PTY size after its
// last input against input from another client.
const sizeHandoffHold = 1500 * time.Millisecond

// LastInputClient returns the client whose input was last written to the
// tab (input_owner.go), or "" when none was.
func (t *Tab) LastInputClient() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastInputClient
}

// noteInputLocked records input written by an owner client: it ranks the
// client as the most recent input client, takes the PTY size when the hold
// allows, and ends a preview guest's control. lastInputClient itself is set
// by noteInputClientLocked right before the input is written. It reports
// whether the published control state changed. The caller holds t.mu.
func (t *Tab) noteInputLocked(client string, now time.Time) bool {
	changed := false
	if t.guestControl != "" || t.guestLease != "" {
		changed = t.guestControl != ""
		t.guestControl = ""
		t.clearGuestLeaseLocked()
	}
	if !t.clientAttachedLocked(client) {
		// A client without a view neither answers nor has a size.
		return changed
	}
	t.inputSeq++
	if t.inputOrder == nil {
		t.inputOrder = make(map[string]uint64)
	}
	t.inputOrder[client] = t.inputSeq
	switch {
	case t.sizeOwner == client:
		t.sizeOwnerInput = now
	case t.sizeOwner == "" || !t.clientAttachedLocked(t.sizeOwner) || now.Sub(t.sizeOwnerInput) >= sizeHandoffHold:
		t.takeSizeLocked(client, now)
		changed = true
	}
	return changed
}

// noteAttachLocked runs when an interactive client's attach commits: the
// client takes the size while nobody owns it. It reports whether the size
// owner changed. The caller holds t.mu.
func (t *Tab) noteAttachLocked(client string, now time.Time) bool {
	if t.sizeOwner != "" {
		return false
	}
	t.takeSizeLocked(client, now)
	return true
}

// takeSizeLocked makes client the size owner; inputAt is its latest input,
// zero for a client promoted without typing. The grid fences a resize
// still in flight for the previous owner; the new owner's view then sends
// its own size. The caller holds t.mu.
func (t *Tab) takeSizeLocked(client string, inputAt time.Time) {
	t.sizeOwner = client
	t.sizeOwnerInput = inputAt
	if t.grid != nil && t.sizeOwned {
		_ = t.grid.Claim()
	}
	t.sizeOwned = true
}

// moreActiveLocked reports whether subscriber a was active more recently
// than b: later input first; between clients without input, the size owner
// (the client already sizing the tab), then the later attach. The caller
// holds t.mu.
func (t *Tab) moreActiveLocked(a, b subscriber) bool {
	orderA, orderB := t.inputOrder[a.client], t.inputOrder[b.client]
	if orderA != orderB {
		return orderA > orderB
	}
	if ownerA, ownerB := a.client == t.sizeOwner, b.client == t.sizeOwner; ownerA != ownerB {
		return ownerA
	}
	return a.attachmentId > b.attachmentId
}

// clientAttachedLocked reports an interactive subscriber of client. The
// caller holds t.mu.
func (t *Tab) clientAttachedLocked(client string) bool {
	for _, subscriber := range t.subscribers {
		if subscriber.client == client && !subscriber.spectator {
			return true
		}
	}
	return false
}

// spectatorOnlyLocked reports a client attached only as a read-only
// spectator. The caller holds t.mu.
func (t *Tab) spectatorOnlyLocked(client string) bool {
	spectator := false
	for _, subscriber := range t.subscribers {
		if subscriber.client != client {
			continue
		}
		if !subscriber.spectator {
			return false
		}
		spectator = true
	}
	return spectator
}

// answererLocked returns the client whose terminal emulator answers queries
// now: among the subscribers that answer and take output, the last input
// client, else the most recently active one. A paused subscriber (refusing
// output) or one whose fanout entry was aborted would answer late or never.
// "" means the headless screen answers. Callers hold t.mu.
func (t *Tab) answererLocked() string {
	var best subscriber
	found := false
	for channelID, subscriber := range t.subscribers {
		if !subscriber.answers || subscriber.paused {
			continue
		}
		if t.fanout != nil && !t.fanout.Has(channelID) {
			continue
		}
		if subscriber.client == t.lastInputClient {
			// The last input client ranks first whenever it can answer.
			return subscriber.client
		}
		if !found || t.moreActiveLocked(subscriber, best) {
			best, found = subscriber, true
		}
	}
	return best.client
}

// releaseDetachedInputLocked forgets the input state of clients that no
// longer have a subscriber. A size owner without a view hands the size to
// the most recently active remaining interactive client; with none left the
// grid keeps the committed size until the next owner. A guest without its
// preview subscription loses control while its lease stays until expiry.
// The caller holds t.mu.
func (t *Tab) releaseDetachedInputLocked() {
	attached := make(map[string]bool, len(t.subscribers))
	for _, subscriber := range t.subscribers {
		attached[subscriber.client] = true
	}
	for client := range t.inputOrder {
		if !attached[client] {
			delete(t.inputOrder, client)
		}
	}
	if t.guestControl != "" && !attached[t.guestControl] {
		t.guestControl = ""
	}
	if t.sizeOwner == "" || t.clientAttachedLocked(t.sizeOwner) {
		return
	}
	var next subscriber
	found := false
	for _, subscriber := range t.subscribers {
		if subscriber.spectator {
			continue
		}
		if !found || t.moreActiveLocked(subscriber, next) {
			next, found = subscriber, true
		}
	}
	if found {
		t.takeSizeLocked(next.client, time.Time{})
		return
	}
	t.sizeOwner = ""
	if t.grid != nil && !t.hidden {
		_ = t.grid.SetMode(terminalgrid.ModeObserver)
	}
}

// resetInputLocked clears the input state of a closed tab. The caller holds
// t.mu.
func (t *Tab) resetInputLocked() {
	t.sizeOwner = ""
	t.guestControl = ""
	t.inputOrder = nil
}
