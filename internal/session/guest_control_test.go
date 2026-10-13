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
	if info := tab.Info(); info.Controller != "" {
		t.Fatalf("precondition: controller = %q, want empty", info.Controller)
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

// TestWriteGuestTakesControlAndWrites pins the basic path: with no controller, a guest write takes
// control (controller=guest, grid same semantics as Claim, emits a control event), bytes land in the PTY
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
	if info := tab.Info(); info.Controller != "preview-share1" {
		t.Fatalf("controller = %q, want preview-share1", info.Controller)
	}
	lease, until := guestLeaseState(tab)
	if lease != "preview-share1" || !until.After(before) {
		t.Fatalf("lease = %q until %v, want preview-share1 after %v", lease, until, before)
	}
	event := <-events
	if event.Controller != "preview-share1" {
		t.Fatalf("control event controller = %q, want preview-share1", event.Controller)
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
	if info := tab.Info(); info.Controller != "preview-share1" {
		t.Fatalf("controller after expired renew = %q", info.Controller)
	}
}

// TestWriteGuestOwnerActive pins owner-active arbitration: when the controller is held by someone else, a guest
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
	if info := tab.Info(); info.Controller != "owner-client" {
		t.Fatalf("controller = %q, want owner-client", info.Controller)
	}
}

// TestWriteGuestGuestBusyUntilLeaseExpiry pins guest-to-guest arbitration: the controlling guest turns away
// other guests (ErrGuestControlOwner); after its disconnect the controller is orphan-cleaned but the lease
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
	if info := tab.Info(); info.Controller != "" {
		t.Fatalf("controller after guest disconnect = %q, want empty", info.Controller)
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
	if info := tab.Info(); info.Controller != "preview-b" {
		t.Fatalf("controller = %q, want preview-b", info.Controller)
	}
	if lease, _ := guestLeaseState(tab); lease != "preview-b" {
		t.Fatalf("lease = %q, want preview-b", lease)
	}
	if got := string(channel.written()); got != "a\nb\n" {
		t.Fatalf("pty input = %q, want %q", got, "a\nb\n")
	}
}

// TestWriteGuestOwnerClaimPreempts pins the owner reclaiming: Claim immediately clears the guest lease and takes over,
// and a further guest write gets ErrGuestControlOwner.
func TestWriteGuestOwnerClaimPreempts(t *testing.T) {
	manager, _ := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-preempt", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)

	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("ls\n"), time.Hour); err != nil {
		t.Fatal(err)
	}
	previous, err := manager.Claim(tab.ID, "owner-client")
	if err != nil {
		t.Fatal(err)
	}
	if previous != "preview-share1" {
		t.Fatalf("Claim previous = %q, want preview-share1", previous)
	}
	if info := tab.Info(); info.Controller != "owner-client" {
		t.Fatalf("controller = %q, want owner-client", info.Controller)
	}
	if lease, _ := guestLeaseState(tab); lease != "" {
		t.Fatalf("Claim left guest lease %q", lease)
	}
	err = manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("ls\n"), time.Hour)
	if !errors.Is(err, ErrGuestControlOwner) {
		t.Fatalf("WriteGuest after owner claim = %v, want ErrGuestControlOwner", err)
	}
}

// TestReleaseGuest pins the release semantics: control is handed back (grid observer semantics + control event) and
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
	if info := tab.Info(); info.Controller != "preview-a" {
		t.Fatalf("unrelated ReleaseGuest changed controller to %q", info.Controller)
	}

	drainControlEvents(events)
	if err := manager.ReleaseGuest(tab.ID, "preview-a"); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Controller != "" {
		t.Fatalf("controller after release = %q, want empty", info.Controller)
	}
	if lease, _ := guestLeaseState(tab); lease != "" {
		t.Fatalf("release left lease %q", lease)
	}
	if event := <-events; event.Controller != "" {
		t.Fatalf("release event controller = %q, want empty", event.Controller)
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
	if info := tab.Info(); info.Controller != "preview-b" {
		t.Fatalf("controller = %q, want preview-b", info.Controller)
	}
}

// TestWriteGuestDisconnectCleanup pins the disconnect interaction: when a guest's subscription channel drops, orphan cleanup
// removes the controller but keeps the lease (reconnect grace), the same guest can resume immediately, other guests are blocked
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
	if info := tab.Info(); info.Controller != "" {
		t.Fatalf("controller after disconnect = %q, want empty", info.Controller)
	}
	if lease, _ := guestLeaseState(tab); lease != "preview-share1" {
		t.Fatalf("lease after disconnect = %q, want preview-share1 (grace window)", lease)
	}

	// The same guest resumes control within the grace window.
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("x"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Controller != "preview-share1" {
		t.Fatalf("controller after resume = %q", info.Controller)
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
	if info := tab.Info(); info.Controller != "preview-other" {
		t.Fatalf("controller = %q, want preview-other", info.Controller)
	}
}

// TestWriteGuestUnsupportedKinds pins the capability boundary: KindRemote shells and non-ptyBacked
// (WinRM) tabs always return ErrUnsupported, even when the controller is free or held.
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
// Claim/Release concurrently, exposing concurrent access issues in the lease fields with -race.
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
			_, _ = manager.Claim(tab.ID, "owner-client")
			_, _ = manager.Release(tab.ID, "owner-client")
		}
	}()
	wg.Wait()
}
