package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func controlEventRecorder(events chan<- ControlEvent) Emitter {
	return EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic == TopicTerminalControl {
			events <- event.Payload.(ControlEvent)
		}
		return nil
	})
}

func newGuestControlManager(t *testing.T, events chan ControlEvent) (*Manager, *fakeConnector) {
	t.Helper()
	connector := newFakeConnector()
	config := Config{Connector: connector, Terminals: newFakeTerminalFactory()}
	if events != nil {
		config.Emitter = controlEventRecorder(events)
	}
	manager := NewManager(config)
	t.Cleanup(func() { _ = manager.Close() })
	return manager, connector
}

func openGuestArbitrationTab(t *testing.T, manager *Manager, session *Session) *Tab {
	t.Helper()
	tab := openTestTab(t, manager, session, "owner-client", "owner-1")
	if err := manager.DetachChannel("owner-1"); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.GuestControl != "" || info.SizeOwner != "" {
		t.Fatalf("precondition: guest control = %q size owner = %q, want empty", info.GuestControl, info.SizeOwner)
	}
	return tab
}

func guestLeaseState(tab *Tab) (string, time.Time) {
	tab.mu.Lock()
	defer tab.mu.Unlock()
	return tab.guestLease, tab.guestLeaseUntil
}

func forceExpireGuestLease(t *testing.T, tab *Tab) {
	t.Helper()
	tab.mu.Lock()
	defer tab.mu.Unlock()
	if tab.guestLease == "" {
		t.Fatal("precondition: no guest lease to expire")
	}
	tab.guestLeaseUntil = time.Now().Add(-time.Second)
}

func drainControlEvents(events chan ControlEvent) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

// TestWriteGuestTakesControlAndWrites pins the basic path: with no owner attached, a guest write takes
// input control (emits a control event), bytes land in the PTY
// channel, and a second write from the same guest renews the lease (expiry advances, no repeated event); after
// lease expiry the same guest can still renew and resume.
func TestWriteGuestTakesControlAndWrites(t *testing.T) {
	events := make(chan ControlEvent, 64)
	manager, connector := newGuestControlManager(t, events)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-basic", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)
	channel := connector.transport(0).channel(0)

	drainControlEvents(events)
	before := time.Now()
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("ls\n"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := string(channel.written()); got != "ls\n" {
		t.Fatalf("pty input = %q, want %q", got, "ls\n")
	}
	if info := tab.Info(); info.GuestControl != "preview-share1" {
		t.Fatalf("guest control = %q, want preview-share1", info.GuestControl)
	}
	lease, until := guestLeaseState(tab)
	if lease != "preview-share1" || !until.After(before) {
		t.Fatalf("lease = %q until %v, want preview-share1 after %v", lease, until, before)
	}
	event := <-events
	if event.GuestControl != "preview-share1" {
		t.Fatalf("control event guest control = %q, want preview-share1", event.GuestControl)
	}

	// The same guest renews: the expiry advances and no control event is emitted again.
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("pwd\n"), 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := string(channel.written()); got != "ls\npwd\n" {
		t.Fatalf("pty input = %q, want %q", got, "ls\npwd\n")
	}
	_, until2 := guestLeaseState(tab)
	if !until2.After(until) {
		t.Fatalf("renewal did not advance lease: %v -> %v", until, until2)
	}
	select {
	case unexpected := <-events:
		t.Fatalf("renewal emitted unexpected control event: %+v", unexpected)
	default:
	}

	// After lease expiry the same guest can still write and resume.
	forceExpireGuestLease(t, tab)
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("again\n"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.GuestControl != "preview-share1" {
		t.Fatalf("guest control after expired renew = %q", info.GuestControl)
	}
}

// TestWriteGuestOwnerActive pins owner-active arbitration: while an owner client is attached, a guest
// write returns ErrGuestControlOwner, bytes do not reach the channel, and no lease is recorded.
func TestWriteGuestOwnerActive(t *testing.T) {
	manager, connector := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-owner", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "owner-client", "owner-1")
	channel := connector.transport(0).channel(0)

	err = manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("ls\n"), time.Minute)
	if !errors.Is(err, ErrGuestControlOwner) {
		t.Fatalf("WriteGuest = %v, want ErrGuestControlOwner", err)
	}
	if got := channel.written(); len(got) != 0 {
		t.Fatalf("rejected write reached pty: %q", got)
	}
	if lease, _ := guestLeaseState(tab); lease != "" {
		t.Fatalf("rejected write recorded lease %q", lease)
	}
	if info := tab.Info(); info.GuestControl != "" || info.SizeOwner != "owner-client" {
		t.Fatalf("guest control = %q size owner = %q, want none and owner-client", info.GuestControl, info.SizeOwner)
	}
}

// TestWriteGuestGuestBusyUntilLeaseExpiry pins guest-to-guest arbitration: the controlling guest turns away
// other guests (ErrGuestControlOwner); after its disconnect its control is cleaned up but the lease
// remains, so other guests get ErrGuestControlBusy; once the lease expires and is lazily cleared, they can take over immediately.
func TestWriteGuestGuestBusyUntilLeaseExpiry(t *testing.T) {
	manager, connector := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-busy", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)
	channel := connector.transport(0).channel(0)

	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "preview-a", ChannelID: "spec-a"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(ctx, tab.ID, "preview-a", []byte("a\n"), time.Hour); err != nil {
		t.Fatal(err)
	}
	err = manager.WriteGuest(ctx, tab.ID, "preview-b", []byte("b\n"), time.Hour)
	if !errors.Is(err, ErrGuestControlOwner) {
		t.Fatalf("write while guest controls = %v, want ErrGuestControlOwner", err)
	}

	if err := manager.DetachChannel("spec-a"); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.GuestControl != "" {
		t.Fatalf("guest control after guest disconnect = %q, want empty", info.GuestControl)
	}
	err = manager.WriteGuest(ctx, tab.ID, "preview-b", []byte("b\n"), time.Hour)
	if !errors.Is(err, ErrGuestControlBusy) {
		t.Fatalf("write during another guest lease = %v, want ErrGuestControlBusy", err)
	}
	if got := string(channel.written()); got != "a\n" {
		t.Fatalf("rejected write reached pty: %q", got)
	}

	forceExpireGuestLease(t, tab)
	if err := manager.WriteGuest(ctx, tab.ID, "preview-b", []byte("b\n"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.GuestControl != "preview-b" {
		t.Fatalf("guest control = %q, want preview-b", info.GuestControl)
	}
	if lease, _ := guestLeaseState(tab); lease != "preview-b" {
		t.Fatalf("lease = %q, want preview-b", lease)
	}
	if got := string(channel.written()); got != "a\nb\n" {
		t.Fatalf("pty input = %q, want %q", got, "a\nb\n")
	}
}

// TestWriteGuestOwnerInputPreempts pins the owner taking input back: an owner attaching does not take a
// controlling guest's control, but owner input ends it and its lease at once, and a further guest write
// gets ErrGuestControlOwner while the owner stays attached.
func TestWriteGuestOwnerInputPreempts(t *testing.T) {
	manager, connector := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-preempt", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)
	channel := connector.transport(0).channel(0)

	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("ls\n"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "owner-client", ChannelID: "owner-2"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("pwd\n"), time.Hour); err != nil {
		t.Fatalf("controlling guest after owner attach = %v", err)
	}
	if err := manager.Write(ctx, tab.ID, "owner-client", []byte("id\n")); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.GuestControl != "" || info.SizeOwner != "owner-client" {
		t.Fatalf("guest control = %q size owner = %q, want none and owner-client", info.GuestControl, info.SizeOwner)
	}
	if lease, _ := guestLeaseState(tab); lease != "" {
		t.Fatalf("owner input left guest lease %q", lease)
	}
	err = manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("ls\n"), time.Hour)
	if !errors.Is(err, ErrGuestControlOwner) {
		t.Fatalf("WriteGuest after owner input = %v, want ErrGuestControlOwner", err)
	}
	if got := string(channel.written()); got != "ls\npwd\nid\n" {
		t.Fatalf("pty input = %q, want %q", got, "ls\npwd\nid\n")
	}
}

// TestReleaseGuest pins the release semantics: control is handed back (control event) and
// the lease is cleared; repeated calls and calls from unrelated guests are idempotent no-ops; other guests take over immediately after release.
func TestReleaseGuest(t *testing.T) {
	events := make(chan ControlEvent, 64)
	manager, _ := newGuestControlManager(t, events)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-release", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)

	if err := manager.WriteGuest(ctx, tab.ID, "preview-a", []byte("a\n"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReleaseGuest(tab.ID, "preview-b"); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.GuestControl != "preview-a" {
		t.Fatalf("unrelated ReleaseGuest changed guest control to %q", info.GuestControl)
	}

	drainControlEvents(events)
	if err := manager.ReleaseGuest(tab.ID, "preview-a"); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.GuestControl != "" {
		t.Fatalf("guest control after release = %q, want empty", info.GuestControl)
	}
	if lease, _ := guestLeaseState(tab); lease != "" {
		t.Fatalf("release left lease %q", lease)
	}
	if event := <-events; event.GuestControl != "" {
		t.Fatalf("release event guest control = %q, want empty", event.GuestControl)
	}

	if err := manager.ReleaseGuest(tab.ID, "preview-a"); err != nil {
		t.Fatalf("idempotent ReleaseGuest = %v", err)
	}
	select {
	case unexpected := <-events:
		t.Fatalf("idempotent ReleaseGuest emitted unexpected event: %+v", unexpected)
	default:
	}

	if err := manager.WriteGuest(ctx, tab.ID, "preview-b", []byte("b\n"), time.Hour); err != nil {
		t.Fatalf("guest take-over after release: %v", err)
	}
	if info := tab.Info(); info.GuestControl != "preview-b" {
		t.Fatalf("guest control = %q, want preview-b", info.GuestControl)
	}
}

// TestWriteGuestDisconnectCleanup pins the disconnect interaction: when a guest's subscription channel drops, orphan cleanup
// removes guest control but keeps the lease (reconnect grace), the same guest can resume immediately, other guests are blocked
// by busy, and after ReleaseGuest explicitly clears the lease other guests can write immediately.
func TestWriteGuestDisconnectCleanup(t *testing.T) {
	manager, _ := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-disconnect", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)

	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "preview-share1", ChannelID: "spec-1"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("x"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := manager.DetachChannel("spec-1"); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.GuestControl != "" {
		t.Fatalf("guest control after disconnect = %q, want empty", info.GuestControl)
	}
	if lease, _ := guestLeaseState(tab); lease != "preview-share1" {
		t.Fatalf("lease after disconnect = %q, want preview-share1 (grace window)", lease)
	}

	// The same guest resumes control within the grace window.
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("x"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.GuestControl != "preview-share1" {
		t.Fatalf("guest control after resume = %q", info.GuestControl)
	}

	// After another disconnect, an explicit lease release lets other guests take over immediately.
	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "preview-share1", ChannelID: "spec-2"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DetachChannel("spec-2"); err != nil {
		t.Fatal(err)
	}
	err = manager.WriteGuest(ctx, tab.ID, "preview-other", []byte("y"), time.Hour)
	if !errors.Is(err, ErrGuestControlBusy) {
		t.Fatalf("other guest during grace window = %v, want ErrGuestControlBusy", err)
	}
	if err := manager.ReleaseGuest(tab.ID, "preview-share1"); err != nil {
		t.Fatal(err)
	}
	if lease, _ := guestLeaseState(tab); lease != "" {
		t.Fatalf("ReleaseGuest left lease %q", lease)
	}
	if err := manager.WriteGuest(ctx, tab.ID, "preview-other", []byte("y"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.GuestControl != "preview-other" {
		t.Fatalf("guest control = %q, want preview-other", info.GuestControl)
	}
}

// TestWriteGuestUnsupportedKinds pins the capability boundary: KindRemote shells and non-ptyBacked
// (WinRM) tabs always return ErrUnsupported, whoever is attached.
func TestWriteGuestUnsupportedKinds(t *testing.T) {
	manager, _ := newGuestControlManager(t, nil)
	ctx := context.Background()

	remote, err := manager.RegisterRemoteSession(ctx, RemoteSessionOptions{OwnerID: "user-1", Title: "relay", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(ctx, remote.ID, "preview-a", []byte("x"), time.Minute); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("remote WriteGuest = %v, want ErrUnsupported", err)
	}
	if err := manager.ReleaseGuest(remote.ID, "preview-a"); err != nil {
		t.Fatalf("remote ReleaseGuest = %v, want nil no-op", err)
	}

	connected, err := manager.Connect(ctx, Asset{ID: "guest-winrm", Kind: KindWinRM})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "owner-client", "owner-1")
	if err := manager.WriteGuest(ctx, tab.ID, "preview-a", []byte("x"), time.Minute); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("winrm WriteGuest = %v, want ErrUnsupported", err)
	}
}

// TestWriteGuestClosedTab pins the closed boundary: after a tab closes, both write and release are rejected.
func TestWriteGuestClosedTab(t *testing.T) {
	manager, _ := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-closed", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(ctx, tab.ID, "preview-a", []byte("x"), time.Minute); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("WriteGuest on closed tab = %v, want ErrTabNotFound", err)
	}
	if err := manager.ReleaseGuest(tab.ID, "preview-a"); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("ReleaseGuest on closed tab = %v, want ErrTabNotFound", err)
	}
}

// TestWriteGuestConcurrentArbitration shakes guest write/release against owner
// input and attach/detach concurrently, exposing concurrent access issues in the lease fields with -race.
func TestWriteGuestConcurrentArbitration(t *testing.T) {
	manager, _ := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-race", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)

	var wg sync.WaitGroup
	for index := 0; index < 4; index++ {
		guest := fmt.Sprintf("preview-%d", index)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 25; round++ {
				_ = manager.WriteGuest(ctx, tab.ID, guest, []byte("x"), 5*time.Millisecond)
				_ = manager.ReleaseGuest(tab.ID, guest)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for round := 0; round < 25; round++ {
			_ = manager.Write(ctx, tab.ID, "owner-client", []byte("y"))
			_, _ = manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "owner-client", ChannelID: "owner-race"})
			_ = manager.Write(ctx, tab.ID, "owner-client", []byte("y"))
			_ = manager.DetachChannel("owner-race")
		}
	}()
	wg.Wait()
}
