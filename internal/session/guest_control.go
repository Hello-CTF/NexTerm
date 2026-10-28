package session

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrGuestControlOwner rejects a guest write: an owner client is attached, or another guest holds input control.
	ErrGuestControlOwner = errors.New("guest control: owner active")
	// ErrGuestControlBusy rejects a guest write: another guest's lease is still valid (within the disconnect grace window).
	ErrGuestControlBusy = errors.New("guest control: another guest lease active")
)

// WriteGuest writes to the terminal as a preview guest and arbitrates input control by lease: the decision and the takeover complete
// atomically inside tab.mu (lock order matches Write: write gate -> mu -> session.mu after unlocking). A guest may take input control
// only while no owner client is attached interactively and no other guest holds control; on success it holds a leaseTTL lease,
// renewed on each call from the same guest. Owner input ends guest control and its lease (input_clients.go), so an attached owner
// returns ErrGuestControlOwner until it detaches; another guest's unexpired lease returns ErrGuestControlBusy, and expired leases
// are lazily cleared during the check. The guest's clientID is the same as its read-only subscription ("preview-<shareID>"), so the
// cleanup on detach removes guest control when the subscription drops, while the lease is kept until expiry: the same guest can
// resume immediately after reconnecting, and other guests must wait for lease expiry or an explicit ReleaseGuest. Guest input is
// not owner input: it neither answers queries nor sizes the PTY. KindRemote/non-ptyBacked tabs do not support guest control
// (ErrUnsupported).
func (m *Manager) WriteGuest(ctx context.Context, tabID, guest string, data []byte, leaseTTL time.Duration) error {
	m.notifyUserInput(tabID)
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	release, err := tab.acquireWrite(ctx)
	if err != nil {
		return err
	}
	defer release()
	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		return ErrTabClosed
	}
	if !ptyBacked(tab.session.asset.Kind) {
		tab.mu.Unlock()
		return ErrUnsupported
	}
	guestID := clientID(guest)
	now := m.now()
	if tab.guestLease != "" && tab.guestLease != guestID && !now.Before(tab.guestLeaseUntil) {
		tab.clearGuestLeaseLocked()
	}
	takeOver := tab.guestControl != guestID
	if takeOver && (tab.guestControl != "" || tab.hasInteractiveSubscriberLocked()) {
		tab.mu.Unlock()
		return ErrGuestControlOwner
	}
	if tab.guestLease != "" && tab.guestLease != guestID {
		tab.mu.Unlock()
		return ErrGuestControlBusy
	}
	var event ControlEvent
	if takeOver {
		tab.guestControl = guestID
	}
	tab.guestLease = guestID
	tab.guestLeaseUntil = now.Add(leaseTTL)
	channel := tab.channel
	generation := tab.generation
	exited := tab.exited
	if takeOver {
		event = tab.controlEventLocked()
	}
	tab.mu.Unlock()
	if takeOver {
		m.emit(context.Background(), TopicTerminalControl, event)
	}
	tab.session.mu.Lock()
	connected := tab.session.status == StatusConnected && tab.session.generation == generation
	tab.session.mu.Unlock()
	if !connected {
		return ErrDisconnected
	}
	if exited || channel == nil {
		return ErrTabClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeChannelContext(ctx, channel, data); err != nil {
		return err
	}
	m.transcriptInput(ctx, tab, data)
	return nil
}

// ReleaseGuest releases the guest's control and lease and emits a control event; it is idempotent - calling it when
// the guest no longer holds control is a no-op. The lease expires with the release, and other guests can take over
// immediately.
func (m *Manager) ReleaseGuest(tabID, guest string) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	guestID := clientID(guest)
	tab.mu.Lock()
	released := tab.guestControl == guestID
	if released {
		tab.guestControl = ""
	}
	if tab.guestLease == guestID {
		tab.clearGuestLeaseLocked()
	}
	var event ControlEvent
	if released {
		event = tab.controlEventLocked()
	}
	tab.mu.Unlock()
	if released {
		m.emit(context.Background(), TopicTerminalControl, event)
	}
	return nil
}

func (t *Tab) clearGuestLeaseLocked() {
	t.guestLease = ""
	t.guestLeaseUntil = time.Time{}
}
