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
// A client that attaches while no attached client has typed counts as the
// most recent input client, and takes the size while nobody owns it, so a
// single client answers and sizes its tab without typing first.
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

// noteAttachLocked runs when an interactive client's attach commits: it
// counts as the most recent input client while no other attached client
// has typed, and takes the size while nobody owns it. It reports whether
// the size owner changed. The caller holds t.mu.
func (t *Tab) noteAttachLocked(client string, now time.Time) bool {
	typed := false
	for _, subscriber := range t.subscribers {
		if subscriber.client != client && !subscriber.spectator && t.inputOrder[subscriber.client] > 0 {
			typed = true
			break
		}
	}
	if !typed && t.inputOrder[client] == 0 {
		t.inputSeq++
		if t.inputOrder == nil {
			t.inputOrder = make(map[string]uint64)
		}
		t.inputOrder[client] = t.inputSeq
	}
	if t.sizeOwner != "" {
		return false
	}
	t.takeSizeLocked(client, now)
	return true
}

// takeSizeLocked makes client the size owner and applies the size it last
// asked for. The grid fences a resize still in flight for the previous
// owner. The caller holds t.mu.
func (t *Tab) takeSizeLocked(client string, now time.Time) {
	t.sizeOwner = client
	t.sizeOwnerInput = now
	if t.grid == nil {
		t.sizeOwned = true
		return
	}
	if t.sizeOwned {
		_ = t.grid.Claim()
	}
	t.sizeOwned = true
	if size, ok := t.clientSizes[client]; ok {
		_, _ = t.grid.SetDesired(size)
	}
}

// rememberSizeLocked records the size a client asked for, applied when it
// owns the size. The caller holds t.mu.
func (t *Tab) rememberSizeLocked(client string, size terminalgrid.Grid) {
	if t.clientSizes == nil {
		t.clientSizes = make(map[string]terminalgrid.Grid)
	}
	t.clientSizes[client] = size
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
// now: the most recent input client among the subscribers that answer and
// take output. A paused subscriber (refusing output) or one whose fanout
// entry was aborted would answer late or never. "" means the headless
// screen answers. Callers hold t.mu.
func (t *Tab) answererLocked() string {
	answerer := ""
	newest := uint64(0)
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
		if order := t.inputOrder[subscriber.client]; order > newest {
			answerer, newest = subscriber.client, order
		}
	}
	return answerer
}

// releaseDetachedInputLocked forgets the input state of clients that no
// longer have a subscriber: a size owner without a view hands the PTY size
// back (the grid keeps the committed size until the next owner), and a
// guest without its preview subscription loses control while its lease
// stays until expiry. The caller holds t.mu.
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
	for client := range t.clientSizes {
		if !attached[client] {
			delete(t.clientSizes, client)
		}
	}
	if t.guestControl != "" && !attached[t.guestControl] {
		t.guestControl = ""
	}
	if t.sizeOwner == "" || t.clientAttachedLocked(t.sizeOwner) {
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
	t.clientSizes = nil
}
