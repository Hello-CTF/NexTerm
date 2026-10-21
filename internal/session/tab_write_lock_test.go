package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// blockingWriteHook parks the channel Write until release is closed, letting a
// test hold the tab write lock across a stalled transport write.
func blockingWriteHook(started chan<- struct{}, release <-chan struct{}) func([]byte) (int, error) {
	return func(data []byte) (int, error) {
		close(started)
		<-release
		return len(data), nil
	}
}

// T-04: a Write whose context ends while another Write holds the tab write
// lock must return the context error instead of parking on the lock.
func TestManagerWriteLockRespectsContext(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	started := make(chan struct{})
	release := make(chan struct{})
	channel.writeHook = blockingWriteHook(started, release)
	first := make(chan error, 1)
	go func() {
		first <- manager.Write(context.Background(), tab.ID, tab.Info().Controller, []byte("a"))
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first Write never reached the channel")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() {
		second <- manager.Write(ctx, tab.ID, tab.Info().Controller, []byte("b"))
	}()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("second Write = %v, want context deadline exceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Write parked on the tab write lock instead of honoring its context")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first Write = %v", err)
	}
}

// T-04: a Write waiting on the tab write lock must observe the tab closing
// instead of staying parked until the holder finishes.
func TestManagerWriteUnblocksOnTabClose(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	started := make(chan struct{})
	channel.writeHook = func(data []byte) (int, error) {
		close(started)
		// Park like a stalled transport write until the channel closes.
		<-channel.closed
		return len(data), nil
	}
	first := make(chan error, 1)
	go func() {
		first <- manager.Write(context.Background(), tab.ID, tab.Info().Controller, []byte("a"))
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first Write never reached the channel")
	}
	second := make(chan error, 1)
	go func() {
		second <- manager.Write(context.Background(), tab.ID, tab.Info().Controller, []byte("b"))
	}()
	// Let the queued Write reach the write gate before the tab goes away, so
	// the close — not the lookup — is what must wake it.
	time.Sleep(50 * time.Millisecond)
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatalf("CloseTab = %v", err)
	}
	select {
	case err := <-second:
		// Depending on scheduling the queued Write either observed the close
		// (ErrTabClosed) or the removal (ErrTabNotFound); both prove it did
		// not park on the lock.
		if !errors.Is(err, ErrTabClosed) && !errors.Is(err, ErrTabNotFound) {
			t.Fatalf("queued Write = %v, want ErrTabClosed or ErrTabNotFound", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued Write stayed parked after the tab closed")
	}
	if err := <-first; err != nil {
		t.Fatalf("first Write = %v", err)
	}
}

// T-04 invariant: input the transport never confirmed must not be replayed
// into the channel later; only confirmed bytes reach the tab.
func TestManagerWriteDoesNotReplayUnconfirmedInput(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	channel.writeErr = errors.New("transport lost")
	if err := manager.Write(context.Background(), tab.ID, tab.Info().Controller, []byte("unconfirmed")); err == nil {
		t.Fatal("Write succeeded against a failing channel")
	}
	channel.writeErr = nil
	if err := manager.Write(context.Background(), tab.ID, tab.Info().Controller, []byte("confirmed")); err != nil {
		t.Fatalf("Write after recovery = %v", err)
	}
	if got := string(channel.written()); got != "confirmed" {
		t.Fatalf("channel received %q; unconfirmed input must not be replayed", got)
	}
}

// T-04: the terminal response writer shares the same write lock as user
// input, so a stalled response write cannot interleave with a later Write.
func TestManagerResponseWriterSharesWriteLock(t *testing.T) {
	manager, connector, terminals, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	started := make(chan struct{})
	release := make(chan struct{})
	channel.writeHook = blockingWriteHook(started, release)
	term := terminals.terminal(tab.ID)
	response := make(chan struct{})
	go func() {
		term.mu.Lock()
		handler := term.response
		term.mu.Unlock()
		if handler != nil {
			handler([]byte("\x1b[>0c"))
		}
		close(response)
	}()
	select {
	case <-response:
	case <-time.After(5 * time.Second):
		t.Fatal("response handler never ran")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("response write never reached the channel")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() {
		second <- manager.Write(ctx, tab.ID, tab.Info().Controller, []byte("b"))
	}()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Write = %v, want context deadline exceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Write parked behind the stalled response write")
	}
	close(release)
}

// L-03: a tab whose channel close fails must keep the closeError visible and
// stay retryable; a later close retries the channel instead of dropping the
// tab as if the first close had succeeded.
func TestCloseTabRetriesFailedChannelClose(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	attempts := 0
	channel.closeHook = func() error {
		attempts++
		if attempts == 1 {
			return errors.New("channel close failed")
		}
		return nil
	}
	if err := manager.CloseTab(tab.ID); err == nil {
		t.Fatal("first close swallowed the channel close failure")
	}
	if _, err := manager.Tab(tab.ID); err != nil {
		t.Fatalf("tab dropped after a failed close; retry state must keep it: %v", err)
	}
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatalf("retry close = %v", err)
	}
	if _, err := manager.Tab(tab.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("tab survived a successful retry close: %v", err)
	}
}

// L-03: a session teardown whose transport close fails must surface the
// failure to the caller (account cleanup and shutdown included) instead of
// reporting a clean close.
func TestManagerCloseSurfacesTransportCloseFailure(t *testing.T) {
	connector := newFakeConnector()
	connector.connect = func(ctx context.Context, asset Asset, generation uint64, call int) (base.Transport, error) {
		transport := newFakeTransport(asset.Kind, generation)
		transport.closeErr = errors.New("transport close failed")
		return transport, nil
	}
	manager := NewManager(Config{Connector: connector, Terminals: terminalFactory{}})
	if _, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err == nil {
		t.Fatal("Manager.Close swallowed the transport close failure")
	}
}

// L-03: a user-initiated disconnect whose channel or transport close fails
// must surface the failure instead of reporting a clean disconnect; the
// session still completes the transition to disconnected.
func TestDisconnectSurfacesCloseFailures(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	channelErr := errors.New("channel close failed")
	transportErr := errors.New("transport close failed")
	channel.closeErr = channelErr
	connector.transport(0).closeErr = transportErr

	err := manager.Disconnect(session.ID)
	if !errors.Is(err, channelErr) || !errors.Is(err, transportErr) {
		t.Fatalf("Disconnect = %v, want channel and transport close failures", err)
	}
	current, getErr := manager.Session(session.ID)
	if getErr != nil {
		t.Fatalf("Session after disconnect = %v", getErr)
	}
	if current.Info().Status != StatusDisconnected {
		t.Fatalf("session status = %s, want disconnected", current.Info().Status)
	}
	_ = tab
}

// A healthy disconnect closes channels and transport cleanly and reports no
// error.
func TestDisconnectHealthyCloseReportsNoError(t *testing.T) {
	manager, _, _, session := openTestSession(t, KindSSH)
	_ = openTestTab(t, manager, session, "client-a", "a-1")
	if err := manager.Disconnect(session.ID); err != nil {
		t.Fatalf("Disconnect = %v, want nil", err)
	}
}
