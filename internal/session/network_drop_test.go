package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// failingReadAttachment makes the first durable attachment's Read fail with err
// once fire is closed, the way a supervisor Stream does when its connection
// dies (readErr) or when a write's context ends (Stream.fail(context.Cause)).
type failingReadAttachment struct {
	*fakeDurableAttachment
	err  error
	fire chan struct{}
}

func (a *failingReadAttachment) Read(buffer []byte) (int, error) {
	select {
	case <-a.fire:
		return 0, a.err
	default:
	}
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := a.fakeDurableAttachment.Read(buffer)
		done <- result{n, err}
	}()
	select {
	case <-a.fire:
		return 0, a.err
	case r := <-done:
		return r.n, r.err
	}
}

type failingReadProvider struct {
	*listingDurableProvider
	mu     sync.Mutex
	first  *failingReadAttachment
	readEr error
}

func (p *failingReadProvider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	attachment, err := p.listingDurableProvider.Create(ctx, options)
	if err != nil {
		return nil, err
	}
	wrapped := &failingReadAttachment{fakeDurableAttachment: attachment.(*fakeDurableAttachment), err: p.readEr, fire: make(chan struct{})}
	p.mu.Lock()
	p.first = wrapped
	p.mu.Unlock()
	return wrapped, nil
}

func openReproDurableTab(t *testing.T, readErr error, connector *fakeConnector, max int) (*Manager, *failingReadProvider, *Session, TabInfo) {
	t.Helper()
	provider := &failingReadProvider{listingDurableProvider: &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}, readEr: readErr}
	manager := NewManager(Config{
		Connector:        connector,
		Terminals:        newFakeTerminalFactory(),
		DurableResolver:  &countingResolver{provider: provider},
		ReconnectBackoff: []time.Duration{10 * time.Millisecond},
		ReconnectMax:     max,
		OpenAccess:       true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "device/dev-repro", Kind: KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "rc-repro", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, provider, connected, info
}

// A durable stream whose read fails for any reason other than a clean exit
// (no exit code) while the host process is alive must never leave the tab
// permanently exited. Expected: the tab reconnects (or at least stays usable).
// The device transport's IsAlive only reflects a local closed flag, and
// supervisor.Stream records context.Cause(ctx) as its read error when a write
// is cancelled/timed out (client.go sendInput/writeWindowed/awaitInputAcks),
// neither of which is base.ErrDisconnected, so runPump takes the exit branch.
func TestDurableReadErrorDoesNotStrandLiveProcess(t *testing.T) {
	cases := map[string]error{
		"write ctx canceled (Stream.fail(ctx cause))": context.Canceled,
		"write ctx deadline":                          context.DeadlineExceeded,
		"raw net reset":                               &net.OpError{Op: "read", Err: errors.New("connection reset by peer")},
		"unexpected eof":                              io.ErrUnexpectedEOF,
	}
	for name, readErr := range cases {
		t.Run(name, func(t *testing.T) {
			manager, provider, _, info := openReproDurableTab(t, readErr, newFakeConnector(), 3)
			provider.mu.Lock()
			first := provider.first
			provider.mu.Unlock()
			close(first.fire)
			time.Sleep(300 * time.Millisecond)
			// give the pump and any reconnect time to settle
			deadline := time.Now().Add(2 * time.Second)
			var exited bool
			var writeErr error
			for time.Now().Before(deadline) {
				tab, err := manager.Tab(info.ID)
				if err != nil {
					t.Fatal(err)
				}
				exited = tab.Info().Exited
				writeErr = manager.Write(context.Background(), info.ID, "client-a", []byte("x"))
				if !exited && writeErr == nil {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatalf("live durable process stranded: tab exited=%v, write err=%v (ErrTabClosed=%v)", exited, writeErr, errors.Is(writeErr, ErrTabClosed))
		})
	}
}

// Closing the stranded tab (UI sends no mode for an exited tab, and the
// backend maps that to CloseTab) must not kill a process the client never saw
// exit; the process is only released.
func TestClosingStrandedTabDoesNotKillHostProcess(t *testing.T) {
	manager, provider, _, info := openReproDurableTab(t, context.Canceled, newFakeConnector(), 3)
	provider.mu.Lock()
	first := provider.first
	provider.mu.Unlock()
	close(first.fire)
	time.Sleep(300 * time.Millisecond)
	tab, _ := manager.Tab(info.ID)
	if !tab.Info().Exited {
		t.Skip("tab was not stranded; see TestDurableReadErrorDoesNotStrandLiveProcess")
	}
	if err := manager.CloseTab(info.ID); err != nil {
		t.Fatal(err)
	}
	provider.listingDurableProvider.mu.Lock()
	killed := provider.records[info.ID].killed
	provider.listingDurableProvider.mu.Unlock()
	if killed != 0 {
		t.Fatalf("closing an exited-by-error durable tab killed the host process %d time(s)", killed)
	}
}

// After reconnect attempts are exhausted the session is Failed. Network
// recovery (client "online" => ProbeLivenessFor) must bring the still-alive
// durable terminals back; today a non-connected session is reported unchanged.
func TestFailedSessionRecoversWhenNetworkReturns(t *testing.T) {
	connector := newFakeConnector()
	var down sync.Mutex
	offline := true
	connector.connect = func(_ context.Context, asset Asset, _ uint64, call int) (base.Transport, error) {
		down.Lock()
		defer down.Unlock()
		if call > 1 && offline {
			return nil, ipc.NewError(ipc.CodeDisconnected, "offline")
		}
		return newFakeTransport(asset.Kind, uint64(call)+1000), nil
	}
	manager, _, connected, info := openReproDurableTab(t, io.EOF, connector, 2)
	if err := manager.StartReconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, manager, connected.ID, StatusFailed)
	down.Lock()
	offline = false
	down.Unlock()
	if _, err := manager.ProbeLivenessFor(WithOpenAccess(context.Background()), connected.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range manager.ListSessions() {
			if s.ID == connected.ID && s.Status == StatusConnected {
				tab, err := manager.Tab(info.ID)
				if err == nil && !tab.Info().Exited {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	var status Status
	for _, s := range manager.ListSessions() {
		if s.ID == connected.ID {
			status = s.Status
		}
	}
	t.Fatalf("session stays %s after the network returned and the client probed; no automatic path re-attaches the live durable tab", status)
}

// A failed write must map to a disconnect/closed class, never to the
// "内部错误" internal fallback.
func TestWriteErrorsDoNotBecomeInternalError(t *testing.T) {
	for name, err := range map[string]error{
		"context canceled": context.Canceled,
		"wrapped canceled": fmt.Errorf("write: %w", context.Canceled),
		"net closed":       net.ErrClosed,
		"closed pipe":      io.ErrClosedPipe,
		"unexpected eof":   io.ErrUnexpectedEOF,
		"base.ErrClosed":   base.ErrClosed,
	} {
		t.Run(name, func(t *testing.T) {
			if got := IPCError(convertChannelWriteError(err)); got.Code == ipc.CodeInternal {
				t.Fatalf("%v -> %q (%s)", err, got.Message, got.Code)
			}
		})
	}
}

// A durable stream that reports the helper's exited result is positive
// evidence the process ended: the tab exits, the exit is confirmed, and
// closing it cleans the host record up.
func TestDurableExitEvidenceStillEndsTab(t *testing.T) {
	manager, provider, _, info := openReproDurableTab(t, durable.ErrExited, newFakeConnector(), 3)
	provider.mu.Lock()
	first := provider.first
	provider.mu.Unlock()
	close(first.fire)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tab, err := manager.Tab(info.ID)
		if err != nil {
			t.Fatal(err)
		}
		if tab.Info().Exited {
			tab.mu.Lock()
			confirmed := tab.exitConfirmed
			tab.mu.Unlock()
			if !confirmed {
				t.Fatal("exit reported by the helper must be confirmed")
			}
			if err := manager.CloseTab(info.ID); err != nil {
				t.Fatal(err)
			}
			provider.listingDurableProvider.mu.Lock()
			killed := provider.records[info.ID].killed
			provider.listingDurableProvider.mu.Unlock()
			if killed != 1 {
				t.Fatalf("confirmed-exit close killed %d time(s), want 1", killed)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("tab did not exit on helper exit evidence")
}

// A tab left exited without host evidence is released on a plain close and
// only killed when the user asks for it.
func TestCloseUnconfirmedExitedDurableTabDetaches(t *testing.T) {
	for name, kill := range map[string]bool{"close detaches": false, "kill kills": true} {
		t.Run(name, func(t *testing.T) {
			manager, provider, _, info := openReproDurableTab(t, io.EOF, newFakeConnector(), 3)
			tab, err := manager.Tab(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			tab.mu.Lock()
			tab.exited = true
			tab.mu.Unlock()
			if kill {
				err = manager.KillTab(info.ID)
			} else {
				err = manager.CloseTab(info.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			provider.listingDurableProvider.mu.Lock()
			killed := provider.records[info.ID].killed
			provider.listingDurableProvider.mu.Unlock()
			if want := map[bool]int{false: 0, true: 1}[kill]; killed != want {
				t.Fatalf("killed = %d, want %d", killed, want)
			}
			if _, err := manager.Tab(info.ID); err == nil {
				t.Fatal("tab record must be gone after close")
			}
		})
	}
}

// A Failed session holding a durable terminal keeps retrying in the
// background (capped backoff) and recovers without any client probe.
func TestFailedDurableSessionRetriesInBackground(t *testing.T) {
	connector := newFakeConnector()
	var mu sync.Mutex
	offline := true
	attempts := 0
	connector.connect = func(_ context.Context, asset Asset, _ uint64, call int) (base.Transport, error) {
		mu.Lock()
		defer mu.Unlock()
		if call > 1 && offline {
			attempts++
			return nil, ipc.NewError(ipc.CodeDisconnected, "offline")
		}
		return newFakeTransport(asset.Kind, uint64(call)+1000), nil
	}
	manager, _, connected, info := openReproDurableTab(t, io.EOF, connector, 2)
	manager.failedRetryMax = 40 * time.Millisecond
	if err := manager.StartReconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, manager, connected.ID, StatusFailed)
	// Retries are paced by the capped backoff: a few rounds, not a storm.
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	seen := attempts
	offline = false
	mu.Unlock()
	if seen < 4 {
		t.Fatalf("only %d dial attempts while Failed; background retry did not run", seen)
	}
	if seen > 60 {
		t.Fatalf("%d dial attempts in 300ms: retry is not paced", seen)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range manager.ListSessions() {
			if s.ID == connected.ID && s.Status == StatusConnected {
				if tab, err := manager.Tab(info.ID); err == nil && !tab.Info().Exited {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("session did not recover by itself once the network returned")
}

// Recovering a terminal from the background must not be blocked by a stale
// local record of it that an unreachable session left behind.
func TestRecoverReclaimsStaleLocalTab(t *testing.T) {
	manager, _, connected, info := openReproDurableTab(t, io.EOF, newFakeConnector(), 3)
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := manager.Session(connected.ID)
	tab.mu.Lock()
	tab.exited = true
	tab.subscribers = nil
	tab.mu.Unlock()
	session.mu.Lock()
	session.status = StatusFailed
	session.mu.Unlock()
	manager.reclaimStaleLocalTab(WithOpenAccess(context.Background()), info.ID)
	if _, err := manager.Tab(info.ID); err == nil {
		t.Fatal("stale local tab still blocks recovery")
	}
}
