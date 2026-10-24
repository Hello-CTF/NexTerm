package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/pty"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

func openExplicitBackgroundTab(t *testing.T, manager *Manager, connected *Session, client, channel string, retainable bool) *Tab {
	t.Helper()
	opened, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, ClientID: client, ChannelID: channel, Cols: 80, Rows: 24,
		Ephemeral: true, Retainable: retainable,
	})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func explicitRetentionState(tab *Tab) (bool, int) {
	tab.mu.Lock()
	defer tab.mu.Unlock()
	return tab.retained, len(tab.subscribers)
}

func waitForLifecycleResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("tab lifecycle operation deadlocked")
		return nil
	}
}

func waitForRealLocalCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("real local PTY condition did not become true")
}

type retryCloseChannel struct {
	base.Channel
	mu       sync.Mutex
	failures int
	hold     bool
	err      error
	calls    int
}

func (c *retryCloseChannel) Close() error {
	c.mu.Lock()
	c.calls++
	if c.hold {
		err := c.err
		c.mu.Unlock()
		return err
	}
	if c.failures > 0 {
		c.failures--
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()
	return c.Channel.Close()
}

func (c *retryCloseChannel) allow() {
	c.mu.Lock()
	c.hold = false
	c.mu.Unlock()
}

func (c *retryCloseChannel) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type retryCloseTransport struct {
	base.Transport
	mu       sync.Mutex
	failures int
	hold     bool
	err      error
}

func (t *retryCloseTransport) Close() error {
	t.mu.Lock()
	if t.hold {
		err := t.err
		t.mu.Unlock()
		return err
	}
	if t.failures > 0 {
		t.failures--
		err := t.err
		t.mu.Unlock()
		return err
	}
	t.mu.Unlock()
	return t.Transport.Close()
}

func (t *retryCloseTransport) allow() {
	t.mu.Lock()
	t.hold = false
	t.mu.Unlock()
}

func (t *retryCloseTransport) OpenPTY(ctx context.Context, options base.PTYOptions) (base.Channel, error) {
	channel, err := t.Transport.(base.PTYTransport).OpenPTY(ctx, options)
	if err != nil {
		return nil, err
	}
	return &retryCloseChannel{Channel: channel, failures: t.failures, err: t.err}, nil
}

func TestCloseTabFailureRetainsPartialTabForRetry(t *testing.T) {
	injected := errors.New("injected scan failure")
	fakeConnector := newFakeConnector()
	connector := ConnectorFunc(func(ctx context.Context, asset Asset, generation uint64) (base.Transport, error) {
		transport, err := fakeConnector.Connect(ctx, asset, generation)
		if err != nil {
			return nil, err
		}
		return &retryCloseTransport{Transport: transport, failures: 1, err: injected}, nil
	})
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "close-retry", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "close-retry", true)
	handle := tab.channel
	retry := handle.Channel.(*retryCloseChannel)
	underlying := retry.Channel
	t.Cleanup(func() { _ = underlying.Close() })

	if err := manager.CloseTab(tab.ID); !errors.Is(err, injected) {
		t.Fatalf("first CloseTab error = %v, want injected failure", err)
	}
	current, err := manager.Tab(tab.ID)
	if err != nil {
		t.Fatalf("failed CloseTab removed tab: %v", err)
	}
	current.mu.Lock()
	partial := current.destroying && !current.closed && current.channel == handle
	current.mu.Unlock()
	if !partial {
		t.Fatalf("failed CloseTab did not retain retryable partial state")
	}
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatalf("retry CloseTab error = %v", err)
	}
	if _, err := manager.Tab(tab.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("successfully retried tab still listed: %v", err)
	}
	if got := retry.callCount(); got != 2 {
		t.Fatalf("channel Close calls = %d, want failed attempt and retry", got)
	}
}

func TestCloseTabRetriesRealLocalChannelFailures(t *testing.T) {
	injected := errors.New("injected real channel close failure")
	connector := ConnectorFunc(func(_ context.Context, _ Asset, _ uint64) (base.Transport, error) {
		return &retryCloseTransport{Transport: local.NewWithConfig(local.Config{}), failures: 2, err: injected}, nil
	})
	manager := NewManager(Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "real-channel-close-retry", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "real-channel-close-retry", true)
	retry := tab.channel.Channel.(*retryCloseChannel)
	t.Cleanup(func() { _ = retry.Channel.Close() })
	for attempt := 1; attempt <= 2; attempt++ {
		if err := manager.CloseTab(tab.ID); !errors.Is(err, injected) {
			t.Fatalf("CloseTab attempt %d error = %v, want injected failure", attempt, err)
		}
		if _, err := manager.Tab(tab.ID); err != nil {
			t.Fatalf("failed CloseTab removed tab on attempt %d: %v", attempt, err)
		}
	}
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatalf("third CloseTab error = %v", err)
	}
	if _, err := manager.Tab(tab.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("successfully closed tab still listed: %v", err)
	}
	if got := retry.callCount(); got != 3 {
		t.Fatalf("real local Close calls = %d, want two failures and one success", got)
	}
}

func TestDisconnectRetriesRealLocalCleanupFailures(t *testing.T) {
	injected := errors.New("injected real local cleanup failure")
	var transport *retryCloseTransport
	connector := ConnectorFunc(func(_ context.Context, _ Asset, _ uint64) (base.Transport, error) {
		transport = &retryCloseTransport{Transport: local.NewWithConfig(local.Config{}), hold: true, err: injected}
		return transport, nil
	})
	manager := NewManager(Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "disconnect-close-retry", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "disconnect-close-retry", true)
	retry := tab.channel.Channel.(*retryCloseChannel)
	retry.hold = true
	t.Cleanup(func() { _ = retry.Channel.Close() })

	for attempt := 1; attempt <= 2; attempt++ {
		if err := manager.Disconnect(connected.ID); !errors.Is(err, injected) {
			t.Fatalf("Disconnect attempt %d error = %v, want injected failure", attempt, err)
		}
		if _, err := manager.Session(connected.ID); err != nil {
			t.Fatalf("failed Disconnect removed session on attempt %d: %v", attempt, err)
		}
		if _, err := manager.Tab(tab.ID); err != nil {
			t.Fatalf("failed Disconnect removed tab on attempt %d: %v", attempt, err)
		}
	}
	transport.allow()
	retry.allow()
	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatalf("third Disconnect error = %v", err)
	}
	if _, err := manager.Session(connected.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("successfully reaped session still listed: %v", err)
	}
	if _, err := manager.Tab(tab.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("successfully reaped tab still listed: %v", err)
	}
}

func TestManagerCloseReturnsCleanupErrorAndAllowsRetry(t *testing.T) {
	injected := errors.New("injected manager close failure")
	var transport *retryCloseTransport
	connector := ConnectorFunc(func(_ context.Context, _ Asset, _ uint64) (base.Transport, error) {
		transport = &retryCloseTransport{Transport: local.NewWithConfig(local.Config{}), hold: true, err: injected}
		return transport, nil
	})
	manager := NewManager(Config{Connector: connector})
	connected, err := manager.Connect(context.Background(), Asset{ID: "manager-close-retry", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "manager-close-retry", true)
	retry := tab.channel.Channel.(*retryCloseChannel)
	retry.hold = true
	t.Cleanup(func() {
		transport.allow()
		retry.allow()
		_ = retry.Channel.Close()
		_ = manager.Close()
	})

	first := make(chan error, 1)
	go func() { first <- manager.Close() }()
	select {
	case err := <-first:
		if !errors.Is(err, injected) {
			t.Fatalf("first Manager.Close error = %v, want injected failure", err)
		}
	case <-time.After(250 * time.Millisecond):
		_ = retry.Channel.Close()
		if err := <-first; err != nil {
			t.Fatalf("Manager.Close after forced cleanup = %v", err)
		}
		t.Fatal("Manager.Close did not return cleanup error")
	}
	if _, err := manager.Session(connected.ID); err != nil {
		t.Fatalf("failed Manager.Close removed session: %v", err)
	}
	if _, err := manager.Tab(tab.ID); err != nil {
		t.Fatalf("failed Manager.Close removed tab: %v", err)
	}
	transport.allow()
	retry.allow()
	if err := manager.Close(); err != nil {
		t.Fatalf("retry Manager.Close error = %v", err)
	}
	if _, err := manager.Session(connected.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("successfully closed session still listed: %v", err)
	}
	if _, err := manager.Tab(tab.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("successfully closed tab still listed: %v", err)
	}
}

func TestTabInfoReportsLocalBackgroundCapability(t *testing.T) {
	manager, _, _, connected := openTestSession(t, KindLocal)
	retainable := openExplicitBackgroundTab(t, manager, connected, "client-a", "retainable-info", true)
	if info := retainable.Info(); !info.Retainable {
		t.Fatalf("ordinary interactive local TabInfo = %+v, want retainable", info)
	}
	unknown := openExplicitBackgroundTab(t, manager, connected, "client-a", "unknown-info", false)
	if info := unknown.Info(); info.Retainable {
		t.Fatalf("unknown local capability TabInfo = %+v, want not retainable", info)
	}
}

func TestLocalOrdinaryDetachStillReapsEphemeralTab(t *testing.T) {
	manager, connector, _, connected := openTestSession(t, KindLocal)
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "ordinary-a", true)
	if err := manager.DetachClient(tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(tab.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("ordinary detach retained ephemeral tab: %v", err)
	}
	if got := connector.transport(0).channel(0).closeCount.Load(); got != 1 {
		t.Fatalf("ordinary detach PTY close count = %d, want 1", got)
	}
}

func TestLocalExplicitBackgroundRetainsOutputInputAndKill(t *testing.T) {
	manager, connector, terminals, connected := openTestSession(t, KindLocal)
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "background-a", true)
	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("before-background")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(terminals.terminal(tab.ID).bytes(), []byte("before-background")) })

	if err := manager.BackgroundTab(context.Background(), tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if retained, subscribers := explicitRetentionState(tab); !retained || subscribers != 0 {
		t.Fatalf("background state retained=%v subscribers=%d", retained, subscribers)
	}
	if info := tab.Info(); info.Exited {
		t.Fatalf("live background tab marked exited: %+v", info)
	}
	if got := channel.closeCount.Load(); got != 0 {
		t.Fatalf("explicit background closed PTY %d times", got)
	}
	if err := manager.BackgroundTab(context.Background(), tab.ID, "client-a"); err != nil {
		t.Fatalf("idempotent background retry = %v", err)
	}
	if got := connector.transport(0).channelCount(); got != 1 {
		t.Fatalf("background retry created shells: PTY count = %d", got)
	}
	if err := manager.BackgroundTab(context.Background(), tab.ID, "client-b"); err == nil {
		t.Fatal("unattached client received idempotent background success")
	}
	if retained, subscribers := explicitRetentionState(tab); !retained || subscribers != 0 {
		t.Fatalf("failed retry changed background state retained=%v subscribers=%d", retained, subscribers)
	}

	if err := channel.emit([]byte("while-background")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(terminals.terminal(tab.ID).bytes(), []byte("while-background")) })
	attached, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{
		ClientID: "client-a", ChannelID: "background-b", ReplayBytes: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if attached.ID != tab.ID || attached.Controller != "client-a" || attached.Subscribers != 1 {
		t.Fatalf("reattached info = %+v", attached)
	}
	receiver := bindTestReceiver(t, manager, "background-b")
	assertFrameData(t, receiver, replayClear)
	replay := receiveTestFrame(t, receiver)
	if !bytes.Contains(replay.Data, []byte("before-background")) || !bytes.Contains(replay.Data, []byte("while-background")) {
		t.Fatalf("background replay = %q", replay.Data)
	}
	if err := manager.Write(context.Background(), tab.ID, "client-a", []byte("after-reattach")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(channel.written(), []byte("after-reattach")) {
		t.Fatalf("reattached input = %q", channel.written())
	}
	if err := manager.DetachClient(tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(tab.ID); err != nil {
		t.Fatalf("retained tab was reaped after reattach detach: %v", err)
	}
	if got := channel.closeCount.Load(); got != 0 {
		t.Fatalf("retained detach closed PTY %d times", got)
	}

	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(tab.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("killed background tab still listed: %v", err)
	}
	if got := channel.closeCount.Load(); got != 1 {
		t.Fatalf("kill PTY close count = %d, want 1", got)
	}
}

func TestLocalExplicitBackgroundRejectsFailuresWithoutMutation(t *testing.T) {
	t.Run("unknown local capability", func(t *testing.T) {
		manager, _, _, connected := openTestSession(t, KindLocal)
		tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "unsupported-local", false)
		if err := manager.BackgroundTab(context.Background(), tab.ID, "client-a"); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("background error = %v, want unsupported", err)
		}
		if retained, subscribers := explicitRetentionState(tab); retained || subscribers != 1 {
			t.Fatalf("rejected background mutated state retained=%v subscribers=%d", retained, subscribers)
		}
	})

	t.Run("detached create", func(t *testing.T) {
		manager, connector, _, connected := openTestSession(t, KindLocal)
		created, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
		if err != nil {
			t.Fatal(err)
		}
		tab, err := manager.Tab(created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.BackgroundTab(context.Background(), tab.ID, "client-a"); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("detached-create background error = %v, want unsupported", err)
		}
		if retained, subscribers := explicitRetentionState(tab); retained || subscribers != 0 {
			t.Fatalf("detached-create background mutated state retained=%v subscribers=%d", retained, subscribers)
		}
		if got := connector.transport(0).channelCount(); got != 1 {
			t.Fatalf("rejected detached-create background opened shells: PTY count = %d", got)
		}
	})

	t.Run("winrm line", func(t *testing.T) {
		manager, _, _, connected := openTestSession(t, KindWinRM)
		tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "unsupported-winrm", false)
		if err := manager.BackgroundTab(context.Background(), tab.ID, "client-a"); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("background error = %v, want unsupported", err)
		}
		if retained, subscribers := explicitRetentionState(tab); retained || subscribers != 1 {
			t.Fatalf("rejected background mutated state retained=%v subscribers=%d", retained, subscribers)
		}
	})

	t.Run("exited", func(t *testing.T) {
		manager, connector, _, connected := openTestSession(t, KindLocal)
		tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "exited-local", true)
		if err := connector.transport(0).channel(0).Close(); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return tab.Info().Exited })
		if err := manager.BackgroundTab(context.Background(), tab.ID, "client-a"); !errors.Is(err, ErrTabClosed) {
			t.Fatalf("exited background error = %v, want closed", err)
		}
		if retained, subscribers := explicitRetentionState(tab); retained || subscribers != 1 {
			t.Fatalf("exited background mutated state retained=%v subscribers=%d", retained, subscribers)
		}
	})

	t.Run("unattached client", func(t *testing.T) {
		manager, _, _, connected := openTestSession(t, KindLocal)
		tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "unattached-local", true)
		if err := manager.BackgroundTab(context.Background(), tab.ID, "client-b"); !errors.Is(err, ErrDisconnected) {
			t.Fatalf("unattached client background error = %v, want disconnected", err)
		}
		if retained, subscribers := explicitRetentionState(tab); retained || subscribers != 1 {
			t.Fatalf("unattached background mutated state retained=%v subscribers=%d", retained, subscribers)
		}
	})

	t.Run("forbidden", func(t *testing.T) {
		manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory()})
		t.Cleanup(func() { _ = manager.Close() })
		alice := ipc.WithUserID(context.Background(), "user-a")
		bob := ipc.WithUserID(context.Background(), "user-b")
		connected, err := manager.Connect(alice, Asset{ID: "device/background-owner", Kind: KindDevice})
		if err != nil {
			t.Fatal(err)
		}
		tab := openTestTab(t, manager, connected, "alice", "owner-a")
		if err := manager.BackgroundTab(bob, tab.ID, "alice"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("cross-user background error = %v, want forbidden", err)
		}
		if retained, subscribers := explicitRetentionState(tab); retained || subscribers != 1 {
			t.Fatalf("forbidden background mutated state retained=%v subscribers=%d", retained, subscribers)
		}
	})
}

func TestNonEphemeralExplicitBackgroundKeepsDetachSemantics(t *testing.T) {
	manager, connector, _, connected := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, connected, "client-a", "ssh-background")
	// A regular SSH PTY ends with its connection; the manager still detaches a viewer on request, but the tab
	// must not advertise itself as able to run in the background.
	if info := tab.Info(); info.Retainable {
		t.Fatalf("ordinary SSH TabInfo = %+v, want not retainable", info)
	}
	if err := manager.BackgroundTab(context.Background(), tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if retained, subscribers := explicitRetentionState(tab); retained || subscribers != 0 {
		t.Fatalf("ordinary SSH background state retained=%v subscribers=%d", retained, subscribers)
	}
	if _, err := manager.Tab(tab.ID); err != nil {
		t.Fatalf("ordinary SSH tab was closed: %v", err)
	}
	if got := connector.transport(0).channel(0).closeCount.Load(); got != 0 {
		t.Fatalf("ordinary SSH detach closed PTY %d times", got)
	}
}

func TestRetainedLocalTabEndsOnDisconnect(t *testing.T) {
	manager, connector, _, connected := openTestSession(t, KindLocal)
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "disconnect-background", true)
	if err := manager.BackgroundTab(context.Background(), tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(tab.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("disconnect retained background tab: %v", err)
	}
	if got := connector.transport(0).channel(0).closeCount.Load(); got != 1 {
		t.Fatalf("disconnect PTY close count = %d, want 1", got)
	}
}

func TestRetainedLocalTabEndsOnManagerClose(t *testing.T) {
	manager, connector, _, connected := openTestSession(t, KindLocal)
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "close-background", true)
	if err := manager.BackgroundTab(context.Background(), tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if got := connector.transport(0).channel(0).closeCount.Load(); got != 1 {
		t.Fatalf("manager close PTY close count = %d, want 1", got)
	}
	if info := tab.Info(); !info.Exited {
		t.Fatalf("manager close left running tab: %+v", info)
	}
}

func TestRetainedLocalTabNaturalExitIsListed(t *testing.T) {
	manager, connector, terminals, connected := openTestSession(t, KindLocal)
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "exit-background", true)
	if err := manager.BackgroundTab(context.Background(), tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if err := connector.transport(0).channel(0).Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.Info().Exited })
	infos := manager.ListTabs()
	if len(infos) != 1 || infos[0].ID != tab.ID || !infos[0].Exited || infos[0].Subscribers != 0 {
		t.Fatalf("naturally exited background list = %+v", infos)
	}
	if got := terminals.terminal(tab.ID).closes(); got != 0 {
		t.Fatalf("natural exit closed terminal resources %d times", got)
	}
}

func TestDetachCleanupRechecksAttachBeforeReap(t *testing.T) {
	manager, connector, _, connected := openTestSession(t, KindLocal)
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "cleanup-a", false)

	tab.lifecycleMu.Lock()
	detached := make(chan error, 1)
	go func() { detached <- manager.DetachClient(tab.ID, "client-a") }()
	waitFor(t, func() bool { return tab.Info().Subscribers == 0 })
	_, attachErr := manager.AttachTab(context.Background(), tab.ID, AttachOptions{
		ClientID: "client-b", ChannelID: "cleanup-b", ReplayBytes: -1,
	})
	tab.lifecycleMu.Unlock()
	if attachErr != nil {
		t.Fatal(attachErr)
	}
	if err := waitForLifecycleResult(t, detached); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Tab(tab.ID); err != nil {
		t.Fatalf("stale cleanup killed the reattached tab: %v", err)
	}
	if info := tab.Info(); info.Subscribers != 1 || info.Controller != "client-b" {
		t.Fatalf("reattached tab state = %+v", info)
	}
	if got := connector.transport(0).channel(0).closeCount.Load(); got != 0 {
		t.Fatalf("stale cleanup closed the reattached PTY %d times", got)
	}
}

func TestDetachCleanupBarrierRejectsLateAttachAndBackground(t *testing.T) {
	manager, _, _, connected := openTestSession(t, KindLocal)
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "barrier-cleanup-a", true)
	entered := make(chan struct{})
	release := make(chan struct{})
	oldBarrier := manager.hooks.ephemeralReapAfterCurrent
	manager.hooks.ephemeralReapAfterCurrent = func() {
		close(entered)
		<-release
	}
	t.Cleanup(func() { manager.hooks.ephemeralReapAfterCurrent = oldBarrier })

	detached := make(chan error, 1)
	go func() { detached <- manager.DetachClient(tab.ID, "client-a") }()
	<-entered
	_, attachErr := manager.AttachTab(context.Background(), tab.ID, AttachOptions{
		ClientID: "client-a", ChannelID: "barrier-cleanup-b", ReplayBytes: -1,
	})
	backgroundErr := manager.BackgroundTab(context.Background(), tab.ID, "client-a")
	close(release)
	if err := waitForLifecycleResult(t, detached); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(attachErr, ErrTabClosed) {
		t.Errorf("attach after reaper current check = %v, want closed", attachErr)
	}
	if !errors.Is(backgroundErr, ErrTabClosed) {
		t.Errorf("background after reaper current check = %v, want closed", backgroundErr)
	}
	if _, err := manager.Tab(tab.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("reaped tab still listed: %v", err)
	}
}

func TestDetachCleanupRechecksExplicitBackgroundBeforeReap(t *testing.T) {
	manager, connector, _, connected := openTestSession(t, KindLocal)
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "retain-cleanup-a", true)

	tab.lifecycleMu.Lock()
	detached := make(chan error, 1)
	go func() { detached <- manager.DetachClient(tab.ID, "client-a") }()
	waitFor(t, func() bool { return tab.Info().Subscribers == 0 })
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{
		ClientID: "client-a", ChannelID: "retain-cleanup-b", ReplayBytes: -1,
	}); err != nil {
		tab.lifecycleMu.Unlock()
		t.Fatal(err)
	}
	if err := manager.BackgroundTab(context.Background(), tab.ID, "client-a"); err != nil {
		tab.lifecycleMu.Unlock()
		t.Fatal(err)
	}
	tab.lifecycleMu.Unlock()
	if err := waitForLifecycleResult(t, detached); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Tab(tab.ID); err != nil {
		t.Fatalf("stale cleanup killed explicitly retained tab: %v", err)
	}
	if retained, subscribers := explicitRetentionState(tab); !retained || subscribers != 0 {
		t.Fatalf("retained cleanup state retained=%v subscribers=%d", retained, subscribers)
	}
	if got := connector.transport(0).channel(0).closeCount.Load(); got != 0 {
		t.Fatalf("stale cleanup closed retained PTY %d times", got)
	}
}

func TestDetachCleanupRechecksGenerationBeforeReap(t *testing.T) {
	manager, connector, _, connected := openTestSession(t, KindLocal)
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "generation-cleanup", false)

	tab.lifecycleMu.Lock()
	detached := make(chan error, 1)
	go func() { detached <- manager.DetachClient(tab.ID, "client-a") }()
	waitFor(t, func() bool { return tab.Info().Subscribers == 0 })
	tab.mu.Lock()
	tab.setGenerationLocked(tab.generation + 1)
	tab.mu.Unlock()
	tab.lifecycleMu.Unlock()
	if err := waitForLifecycleResult(t, detached); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Tab(tab.ID); err != nil {
		t.Fatalf("old-generation cleanup killed current tab: %v", err)
	}
	if got := connector.transport(0).channel(0).closeCount.Load(); got != 0 {
		t.Fatalf("old-generation cleanup closed PTY %d times", got)
	}
}

func TestDetachCleanupReapsExitedEphemeralTab(t *testing.T) {
	manager, connector, terminals, connected := openTestSession(t, KindLocal)
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "exited-cleanup", false)
	channel := connector.transport(0).channel(0)

	tab.lifecycleMu.Lock()
	detached := make(chan error, 1)
	go func() { detached <- manager.DetachClient(tab.ID, "client-a") }()
	waitFor(t, func() bool { return tab.Info().Subscribers == 0 })
	if err := channel.Close(); err != nil {
		tab.lifecycleMu.Unlock()
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.Info().Exited })
	tab.lifecycleMu.Unlock()
	if err := waitForLifecycleResult(t, detached); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Tab(tab.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("ordinary exited ephemeral tab was not reaped: %v", err)
	}
	if got := terminals.terminal(tab.ID).closes(); got != 1 {
		t.Fatalf("exited cleanup terminal close count = %d, want 1", got)
	}
}

func TestExplicitBackgroundConcurrentKillAlwaysCloses(t *testing.T) {
	for index := 0; index < 16; index++ {
		manager, connector, _, connected := openTestSession(t, KindLocal)
		tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "kill-race", true)
		start := make(chan struct{})
		backgroundResult := make(chan error, 1)
		killResult := make(chan error, 1)
		go func() {
			<-start
			backgroundResult <- manager.BackgroundTab(context.Background(), tab.ID, "client-a")
		}()
		go func() {
			<-start
			killResult <- manager.CloseTab(tab.ID)
		}()
		close(start)
		if err := waitForLifecycleResult(t, backgroundResult); err != nil && !errors.Is(err, ErrTabNotFound) && !errors.Is(err, ErrTabClosed) {
			t.Fatalf("iteration %d background error = %v", index, err)
		}
		if err := waitForLifecycleResult(t, killResult); err != nil {
			t.Fatalf("iteration %d kill error = %v", index, err)
		}
		if _, err := manager.Tab(tab.ID); !errors.Is(err, ErrTabNotFound) {
			t.Fatalf("iteration %d retained tab survived explicit kill: %v", index, err)
		}
		if got := connector.transport(0).channel(0).closeCount.Load(); got != 1 {
			t.Fatalf("iteration %d kill PTY close count = %d, want 1", index, got)
		}
	}
}

func TestRealLocalPTYExplicitBackgroundCleanupRaceReplayInputAndKill(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("real local shell test requires /bin/sh")
	}
	writes := &lockedWriteLog{}
	connector := ConnectorFunc(func(_ context.Context, _ Asset, _ uint64) (base.Transport, error) {
		return &writeLogTransport{Transport: local.NewWithConfig(local.Config{Shell: "/bin/sh"}), log: writes}, nil
	})
	manager := NewManager(Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "real-explicit-background", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab := openExplicitBackgroundTab(t, manager, connected, "client-a", "real-background-a", true)
	shellReady := realLocalShellReadiness(t, tab, writes, 0)
	write := func(input string) {
		t.Helper()
		shellReady()
		if err := manager.Write(context.Background(), tab.ID, "client-a", []byte(input)); err != nil {
			t.Fatal(err)
		}
	}
	dump := func() []byte { return tab.terminal.Dump(1 << 20) }
	defer func() {
		if t.Failed() {
			t.Logf("real local PTY dump: %q", dump())
			t.Logf("real local PTY input: %q", writes.String())
		}
	}()
	processPresent := func(pid int) bool {
		t.Helper()
		present, err := realLocalProcessPresent(pid)
		if err != nil {
			t.Fatal(err)
		}
		return present
	}
	processGroup := func(pid int) int {
		t.Helper()
		group, state, err := realLocalProcessInfo(pid)
		if err != nil {
			t.Fatal(err)
		}
		if state == "" {
			t.Fatalf("process %d was not found", pid)
		}
		return group
	}

	write(`printf 'nx-real-%s\n' 'ready'` + "\r")
	waitForRealLocalCondition(t, func() bool { return bytes.Contains(dump(), []byte("nx-real-ready")) })
	write(`printf 'nx-real-pid-%s\n' "$$"` + "\r")
	pidPattern := regexp.MustCompile(`nx-real-pid-([0-9]+)`)
	var shellPID string
	waitForRealLocalCondition(t, func() bool {
		match := pidPattern.FindSubmatch(dump())
		if len(match) != 2 {
			return false
		}
		shellPID = string(match[1])
		return true
	})
	pid, err := strconv.Atoi(shellPID)
	if err != nil {
		t.Fatal(err)
	}
	write(`(sleep 1; printf 'nx-real-%s\n' 'after'; sleep 30) & printf 'nx-real-child-%s\n' $!` + "\r")
	childPattern := regexp.MustCompile(`nx-real-child-([0-9]+)`)
	var childPID string
	waitForRealLocalCondition(t, func() bool {
		match := childPattern.FindSubmatch(dump())
		if len(match) != 2 {
			return false
		}
		childPID = string(match[1])
		return true
	})
	child, err := strconv.Atoi(childPID)
	if err != nil {
		t.Fatal(err)
	}
	shellGroup := processGroup(pid)
	childGroup := processGroup(child)
	if childGroup == shellGroup {
		t.Fatalf("default job control did not separate groups: shell=%d child=%d", shellGroup, childGroup)
	}
	t.Cleanup(func() { realLocalBackgroundGroupCleanup(childGroup, shellGroup) })
	if !processPresent(pid) || !processPresent(child) {
		t.Fatalf("real processes before cleanup: shell=%d child=%d", pid, child)
	}

	tab.lifecycleMu.Lock()
	cleanupPaused := true
	defer func() {
		if cleanupPaused {
			tab.lifecycleMu.Unlock()
		}
	}()
	detached := make(chan error, 1)
	go func() { detached <- manager.DetachClient(tab.ID, "client-a") }()
	waitForRealLocalCondition(t, func() bool { return tab.Info().Subscribers == 0 })
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{
		ClientID: "client-a", ChannelID: "real-background-b", ReplayBytes: 1 << 20,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.BackgroundTab(context.Background(), tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if retained, subscribers := explicitRetentionState(tab); !retained || subscribers != 0 {
		t.Fatalf("state while cleanup is paused: retained=%v subscribers=%d", retained, subscribers)
	}
	tab.lifecycleMu.Unlock()
	cleanupPaused = false
	if err := waitForLifecycleResult(t, detached); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(tab.ID); err != nil {
		t.Fatalf("stale real cleanup killed retained tab: %v", err)
	}
	if info := tab.Info(); info.Subscribers != 0 || info.Exited || !info.Ephemeral || info.Durable {
		t.Fatalf("real background state = %+v", info)
	}
	if !processPresent(pid) || !processPresent(child) {
		t.Fatalf("real cleanup killed retained processes: shell=%d child=%d", pid, child)
	}
	waitForRealLocalCondition(t, func() bool { return bytes.Contains(dump(), []byte("nx-real-after")) })

	attached, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{
		ClientID: "client-a", ChannelID: "real-background-c", ReplayBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if attached.ID != tab.ID || attached.Controller != "client-a" {
		t.Fatalf("real reattached info = %+v", attached)
	}
	receiver := bindTestReceiver(t, manager, "real-background-c")
	anchor := receiveTestAnchor(t, receiver)
	if anchor.Expired || anchor.ControlBytes != len(replayClear) {
		t.Fatalf("real background anchor = %+v", anchor)
	}
	assertFrameData(t, receiver, replayClear)
	deadline := time.Now().Add(3 * time.Second)
	for {
		frame := receiveTestFrame(t, receiver)
		if bytes.Contains(frame.Data, []byte("nx-real-ready")) && bytes.Contains(frame.Data, []byte("nx-real-after")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("real background replay missing output: %q", frame.Data)
		}
	}
	write(`printf 'nx-real-input-%s\n' "$$"` + "\r")
	waitForRealLocalCondition(t, func() bool {
		return bytes.Contains(dump(), []byte("nx-real-input-"+shellPID))
	})

	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(tab.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("real killed background tab still listed: %v", err)
	}
	waitForRealLocalCondition(t, func() bool { return !processPresent(pid) && !processPresent(child) })

	ordinaryResponses := bytes.Count(writes.Bytes(), []byte("\x1b["))
	ordinary := openExplicitBackgroundTab(t, manager, connected, "client-a", "real-ordinary-a", true)
	ordinaryReady := realLocalShellReadiness(t, ordinary, writes, ordinaryResponses)
	ordinaryReady()
	ordinaryDump := func() []byte { return ordinary.terminal.Dump(1 << 20) }
	if err := manager.Write(context.Background(), ordinary.ID, "client-a", []byte(`printf 'nx-real-ordinary-%s\n' "$$"`+"\r")); err != nil {
		t.Fatal(err)
	}
	ordinaryPattern := regexp.MustCompile(`nx-real-ordinary-([0-9]+)`)
	var ordinaryPID string
	waitForRealLocalCondition(t, func() bool {
		match := ordinaryPattern.FindSubmatch(ordinaryDump())
		if len(match) != 2 {
			return false
		}
		ordinaryPID = string(match[1])
		return true
	})
	ordinaryProcess, err := strconv.Atoi(ordinaryPID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DetachClient(ordinary.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(ordinary.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("ordinary real ephemeral tab survived detach: %v", err)
	}
	waitForRealLocalCondition(t, func() bool { return !processPresent(ordinaryProcess) })
}

func realLocalProcessSnapshot() ([]byte, error) {
	return osexec.Command("ps", "-A", "-o", "pid=,pgid=,stat=").Output()
}

func realLocalProcessInfo(pid int, snapshots ...func() ([]byte, error)) (int, string, error) {
	snapshot := realLocalProcessSnapshot
	if len(snapshots) > 0 {
		snapshot = snapshots[0]
	}
	output, err := snapshot()
	if err != nil {
		return 0, "", err
	}
	pidText := strconv.Itoa(pid)
	for _, line := range bytes.Split(output, []byte("\n")) {
		fields := bytes.Fields(line)
		if len(fields) == 0 || string(fields[0]) != pidText {
			continue
		}
		if len(fields) != 3 {
			return 0, "", errors.New("unexpected process list line")
		}
		group, err := strconv.Atoi(string(fields[1]))
		if err != nil {
			return 0, "", err
		}
		return group, string(fields[2]), nil
	}
	return 0, "", nil
}

func realLocalProcessPresent(pid int, snapshots ...func() ([]byte, error)) (bool, error) {
	_, state, err := realLocalProcessInfo(pid, snapshots...)
	if err != nil {
		return false, err
	}
	return len(state) > 0 && state[0] != 'Z', nil
}

// realLocalBackgroundGroupCleanup kills the background job's process group,
// but only while a fresh process snapshot still proves the group belongs to
// the child; an unverifiable or unsafe target is left alone rather than risk
// signaling the test runner or an unrelated group.
func realLocalBackgroundGroupCleanup(childGroup, shellGroup int) {
	output, err := realLocalProcessSnapshot()
	if err != nil {
		return
	}
	if !realLocalBackgroundGroupKillSafe(output, childGroup, shellGroup, os.Getpid()) {
		return
	}
	_ = realLocalBackgroundGroupKillCommand(childGroup).Run()
}

// realLocalBackgroundGroupKillCommand builds the process group kill. The "--"
// is mandatory: procps kill reads a bare negative operand as options and then
// signals its own process group via kill(0), wiping out the test runner.
func realLocalBackgroundGroupKillCommand(childGroup int) *osexec.Cmd {
	return osexec.Command("kill", "-KILL", "--", "-"+strconv.Itoa(childGroup))
}

// realLocalBackgroundGroupKillSafe reports whether a SIGKILL to -childGroup
// provably targets only the background job's own process group. The captured
// group must be valid, distinct from the shell's group and from the test
// runner's own group, and a process must still hold the group leader slot
// under the child's PID so a recycled ID cannot point the kill at strangers.
func realLocalBackgroundGroupKillSafe(snapshot []byte, childGroup, shellGroup, selfPID int) bool {
	if childGroup <= 1 || childGroup == shellGroup {
		return false
	}
	selfGroup := 0
	leaderHeld := false
	selfText := strconv.Itoa(selfPID)
	leaderText := strconv.Itoa(childGroup)
	for _, line := range bytes.Split(snapshot, []byte("\n")) {
		fields := bytes.Fields(line)
		if len(fields) != 3 {
			continue
		}
		switch string(fields[0]) {
		case selfText:
			group, err := strconv.Atoi(string(fields[1]))
			if err != nil {
				return false
			}
			selfGroup = group
		case leaderText:
			group, err := strconv.Atoi(string(fields[1]))
			if err != nil || group != childGroup {
				return false
			}
			leaderHeld = true
		}
	}
	return leaderHeld && selfGroup > 0 && childGroup != selfGroup
}

func TestRealLocalProcessPresentReportsSnapshotFailure(t *testing.T) {
	injected := errors.New("injected process snapshot failure")
	present, err := realLocalProcessPresent(1, func() ([]byte, error) { return nil, injected })
	if !errors.Is(err, injected) {
		t.Fatalf("processPresent error = %v, want injected snapshot failure", err)
	}
	if present {
		t.Fatal("processPresent returned present after snapshot failure")
	}
}

func TestRealLocalBackgroundGroupKillSafeVerifiesTargets(t *testing.T) {
	const (
		selfPID    = 9001
		shellGroup = 4200
		childGroup = 4300
	)
	verified := []byte(" 9001  4100 Ss\n 9100  4100 S\n 4200  4200 Ss\n 4300  4300 S\n")
	tests := []struct {
		name       string
		snapshot   []byte
		childGroup int
		shellGroup int
		want       bool
	}{
		{
			name:       "verified child group is killable",
			snapshot:   verified,
			childGroup: childGroup,
			shellGroup: shellGroup,
			want:       true,
		},
		{
			name:       "zombie leader still holds the group ID",
			snapshot:   []byte(" 9001  4100 Ss\n 4300  4300 Z\n"),
			childGroup: childGroup,
			shellGroup: shellGroup,
			want:       true,
		},
		{
			name:       "refuses the test runner group",
			snapshot:   []byte(" 9001  4300 Ss\n 9100  4300 S\n 4300  4300 Ss\n"),
			childGroup: childGroup,
			shellGroup: shellGroup,
			want:       false,
		},
		{
			name:       "refuses the shell group",
			snapshot:   verified,
			childGroup: shellGroup,
			shellGroup: shellGroup,
			want:       false,
		},
		{
			name:       "refuses the init group",
			snapshot:   verified,
			childGroup: 1,
			shellGroup: shellGroup,
			want:       false,
		},
		{
			name:       "refuses a zero group",
			snapshot:   verified,
			childGroup: 0,
			shellGroup: shellGroup,
			want:       false,
		},
		{
			name:       "refuses when the leader is gone",
			snapshot:   []byte(" 9001  4100 Ss\n 4200  4200 Ss\n"),
			childGroup: childGroup,
			shellGroup: shellGroup,
			want:       false,
		},
		{
			name:       "refuses a leader ID recycled into another group",
			snapshot:   []byte(" 9001  4100 Ss\n 4300  4400 S\n"),
			childGroup: childGroup,
			shellGroup: shellGroup,
			want:       false,
		},
		{
			name:       "refuses when the test runner is missing from the snapshot",
			snapshot:   []byte(" 4200  4200 Ss\n 4300  4300 S\n"),
			childGroup: childGroup,
			shellGroup: shellGroup,
			want:       false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := realLocalBackgroundGroupKillSafe(test.snapshot, test.childGroup, test.shellGroup, selfPID); got != test.want {
				t.Fatalf("killSafe(%d, %d) = %v, want %v", test.childGroup, test.shellGroup, got, test.want)
			}
		})
	}
}

func TestRealLocalBackgroundGroupKillCommandSeparatesOperands(t *testing.T) {
	args := realLocalBackgroundGroupKillCommand(4321).Args
	if len(args) != 4 || args[0] != "kill" || args[1] != "-KILL" || args[2] != "--" || args[3] != "-4321" {
		t.Fatalf("kill command args = %v, want [kill -KILL -- -4321]", args)
	}
}

func TestRealLocalBackgroundGroupCleanupKillsOnlyVerifiedHelperGroup(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("real process group cleanup test requires unix kill semantics")
	}
	cmd := osexec.Command("sleep", "30")
	process, err := pty.StartProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Kill() })
	pid := cmd.Process.Pid
	group, state, err := realLocalProcessInfo(pid)
	if err != nil {
		t.Fatal(err)
	}
	if state == "" {
		t.Fatalf("helper process %d was not found", pid)
	}
	if group != pid {
		t.Fatalf("helper process %d is not its own group leader: group=%d", pid, group)
	}
	selfGroup, _, err := realLocalProcessInfo(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if selfGroup <= 0 {
		t.Fatalf("test runner process group = %d, want a positive group", selfGroup)
	}
	if realLocalBackgroundGroupKillSafe(mustRealLocalSnapshot(t), selfGroup, selfGroup+1, os.Getpid()) {
		t.Fatal("killSafe accepted the test runner's own process group")
	}
	realLocalBackgroundGroupCleanup(pid, pid+1)
	waitForRealLocalCondition(t, func() bool {
		present, err := realLocalProcessPresent(pid)
		if err != nil {
			t.Fatal(err)
		}
		return !present
	})
}

func mustRealLocalSnapshot(t *testing.T) []byte {
	t.Helper()
	output, err := realLocalProcessSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return output
}

type lockedWriteLog struct {
	mu sync.Mutex
	bytes.Buffer
}

func (l *lockedWriteLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Buffer.Write(data)
}

func (l *lockedWriteLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Buffer.String()
}

func (l *lockedWriteLog) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.Buffer.Bytes()...)
}

type writeLogTransport struct {
	base.Transport
	log *lockedWriteLog
}

func (t *writeLogTransport) OpenPTY(ctx context.Context, options base.PTYOptions) (base.Channel, error) {
	channel, err := t.Transport.(base.PTYTransport).OpenPTY(ctx, options)
	if channel != nil {
		channel = &writeLogChannel{Channel: channel, log: t.log}
	}
	return channel, err
}

type writeLogChannel struct {
	base.Channel
	log *lockedWriteLog
}

func (c *writeLogChannel) Write(data []byte) (int, error) {
	_, _ = c.log.Write(data)
	return c.Channel.Write(data)
}

func realLocalShellReadiness(t *testing.T, tab *Tab, writes *lockedWriteLog, responses int) func() {
	t.Helper()
	shellHelp, _ := osexec.Command("/bin/sh", "--help").CombinedOutput()
	if !bytes.Contains(shellHelp, []byte("BusyBox")) {
		return func() {}
	}
	queries := 0
	return func() {
		t.Helper()
		queries++
		waitForRealLocalCondition(t, func() bool {
			return bytes.Count(tab.terminal.Dump(1<<20), []byte("\x1b[6n")) >= queries &&
				bytes.Count(writes.Bytes(), []byte("\x1b[")) >= responses+queries
		})
		time.Sleep(100 * time.Millisecond)
	}
}
