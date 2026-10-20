package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestConnectSingleFlightAndConnectionReuse(t *testing.T) {
	connector := newFakeConnector()
	started := make(chan struct{})
	release := make(chan struct{})
	connector.connect = func(ctx context.Context, asset Asset, generation uint64, call int) (base.Transport, error) {
		if call == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return newFakeTransport(asset.Kind, uint64(call)+1000), nil
	}
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	asset := Asset{ID: "asset", Name: "server", Kind: KindSSH}
	const callers = 12
	results := make(chan struct {
		session *Session
		err     error
	}, callers)
	for index := 0; index < callers; index++ {
		go func() {
			session, err := manager.Connect(context.Background(), asset)
			results <- struct {
				session *Session
				err     error
			}{session: session, err: err}
		}()
	}
	<-started
	time.Sleep(20 * time.Millisecond)
	if got := connector.callCount(); got != 1 {
		t.Fatalf("connect calls before release = %d, want 1", got)
	}
	close(release)
	var id string
	for index := 0; index < callers; index++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if id == "" {
			id = result.session.ID
		} else if result.session.ID != id {
			t.Fatalf("asset created sessions %q and %q", id, result.session.ID)
		}
	}
	if got := connector.callCount(); got != 1 {
		t.Fatalf("connect calls = %d, want one shared connection", got)
	}
	if got := len(manager.ListSessions()); got != 1 {
		t.Fatalf("session registry size = %d", got)
	}
}

func TestConnectDropsNewSSHSessionAfterAccountRevocation(t *testing.T) {
	connector := newFakeConnector()
	started := make(chan struct{})
	release := make(chan struct{})
	connector.connect = func(_ context.Context, asset Asset, _ uint64, _ int) (base.Transport, error) {
		close(started)
		<-release
		return newFakeTransport(asset.Kind, 1), nil
	}
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	revoked := make(chan struct{})
	ctx := ipc.WithUserID(t.Context(), "account-a")
	ctx = ipc.WithAccountSessionCheck(ctx, func(context.Context) error {
		select {
		case <-revoked:
			return ErrForbidden
		default:
			return nil
		}
	})
	result := make(chan error, 1)
	go func() {
		_, err := manager.ConnectFor(ctx, Asset{ID: "host-a", Kind: KindSSH})
		result <- err
	}()
	<-started
	close(revoked)
	close(release)
	if err := <-result; !errors.Is(err, ErrForbidden) {
		t.Fatalf("late SSH connect error = %v; want revoked account session", err)
	}
	if connector.transport(0).IsAlive() || len(manager.ListSessions()) != 0 {
		t.Fatal("late SSH connect retained a transport or session")
	}
}

func TestConnectRetriesForValidClientAfterDialingClientWasRevoked(t *testing.T) {
	connector := newFakeConnector()
	started := make(chan struct{})
	release := make(chan struct{})
	connector.connect = func(_ context.Context, asset Asset, _ uint64, call int) (base.Transport, error) {
		if call == 1 {
			close(started)
			<-release
		}
		return newFakeTransport(asset.Kind, uint64(call)), nil
	}
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	revoked := make(chan struct{})
	oldCtx := ipc.WithUserID(t.Context(), "account-a")
	oldCtx = ipc.WithAccountSessionCheck(oldCtx, func(context.Context) error {
		select {
		case <-revoked:
			return ErrForbidden
		default:
			return nil
		}
	})
	newCtx := ipc.WithUserID(t.Context(), "account-a")
	newCtx = ipc.WithAccountSessionCheck(newCtx, func(context.Context) error { return nil })
	asset := Asset{ID: "shared-host", Kind: KindSSH}
	oldResult := make(chan error, 1)
	go func() {
		_, err := manager.ConnectFor(oldCtx, asset)
		oldResult <- err
	}()
	<-started
	newResult := make(chan struct {
		session *Session
		err     error
	}, 1)
	go func() {
		session, err := manager.ConnectFor(newCtx, asset)
		newResult <- struct {
			session *Session
			err     error
		}{session, err}
	}()
	close(revoked)
	close(release)
	if err := <-oldResult; !errors.Is(err, ErrForbidden) {
		t.Fatalf("old client's late connect error = %v", err)
	}
	newConnection := <-newResult
	if newConnection.err != nil || newConnection.session == nil || connector.callCount() != 2 {
		t.Fatalf("valid client did not establish a fresh SSH connection: %+v", newConnection)
	}
	if connector.transport(0).IsAlive() || !connector.transport(1).IsAlive() {
		t.Fatal("revoked client's SSH transport remained alive or valid client's new transport closed")
	}
}

func TestMultiClientReplayControlAndMultipleChannels(t *testing.T) {
	manager, connector, terminals, session := openTestSession(t, KindSSH)
	transport := connector.transport(0)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := transport.channel(0)
	second, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-2"})
	if err != nil {
		t.Fatal(err)
	}
	third, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Subscribers != 2 || second.Viewers != 1 {
		t.Fatalf("same-client channels counted incorrectly: %+v", second)
	}
	if third.Subscribers != 3 || third.Viewers != 2 || third.Controller != "client-a" {
		t.Fatalf("two-client attach info = %+v", third)
	}
	a1 := bindTestReceiver(t, manager, "a-1")
	a2 := bindTestReceiver(t, manager, "a-2")
	b1 := bindTestReceiver(t, manager, "b-1")
	assertFrameData(t, a2, replayClear)
	assertFrameData(t, b1, replayClear)
	raw := []byte{0, 0xff, 'r', 'a', 'w'}
	if err := channel.emit(raw); err != nil {
		t.Fatal(err)
	}
	for _, receiver := range []*hub.Receiver{a1, a2, b1} {
		assertFrameData(t, receiver, raw)
	}
	if err := manager.Write(context.Background(), tab.ID, "client-b", []byte("no")); !errors.Is(err, ErrNotController) {
		t.Fatalf("observer write error = %v", err)
	}
	if err := manager.Resize(context.Background(), tab.ID, "client-b", 100, 30); !errors.Is(err, ErrNotController) {
		t.Fatalf("observer resize error = %v", err)
	}
	previous, err := manager.Claim(tab.ID, "client-b")
	if err != nil || previous != "client-a" {
		t.Fatalf("claim previous = %q, err = %v", previous, err)
	}
	if err := manager.Write(context.Background(), tab.ID, "client-a", []byte("no")); !errors.Is(err, ErrNotController) {
		t.Fatalf("former controller write error = %v", err)
	}
	if err := manager.Write(context.Background(), tab.ID, "client-b", []byte("yes")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Resize(context.Background(), tab.ID, "client-b", 100, 30); err != nil {
		t.Fatal(err)
	}
	if got := channel.written(); string(got) != "yes" {
		t.Fatalf("PTY input = %q", got)
	}
	if err := manager.DetachChannel("a-1"); err != nil {
		t.Fatal(err)
	}
	if got := tab.Info().Controller; got != "client-b" {
		t.Fatalf("unrelated detach changed controller to %q", got)
	}
	if _, err := manager.Claim(tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.DetachChannel("a-2"); err == nil {
		t.Log("a-2 already detached through the explicit channel path")
	}
	if err := manager.DetachClient(tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	info := tab.Info()
	if info.Subscribers != 1 || info.Viewers != 1 || info.Controller != "" {
		t.Fatalf("client detach left inconsistent control state: %+v", info)
	}
	if err := manager.DetachAll(tab.ID); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("background")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(terminals.terminal(tab.ID).bytes(), []byte("background")) })
	resumed, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-c", ChannelID: "c-1", ReplayBytes: -1})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Controller != "client-c" || resumed.Subscribers != 1 {
		t.Fatalf("resume info = %+v", resumed)
	}
	c1 := bindTestReceiver(t, manager, "c-1")
	assertFrameData(t, c1, replayClear)
	replay := receiveTestFrame(t, c1)
	if !bytes.Contains(replay.Data, raw) || !bytes.Contains(replay.Data, []byte("background")) {
		t.Fatalf("raw replay lost bytes: %q", replay.Data)
	}
}

func TestReconnectCancellationAndSuccessfulPTYReopen(t *testing.T) {
	connector := newFakeConnector()
	reconnectStarted := make(chan struct{})
	releaseReconnect := make(chan struct{})
	connector.connect = func(_ context.Context, asset Asset, generation uint64, call int) (base.Transport, error) {
		if call == 2 {
			close(reconnectStarted)
			<-releaseReconnect
		}
		return newFakeTransport(asset.Kind, uint64(call)+1000), nil
	}
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), ReconnectMax: 2,
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset", Name: "server", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	firstTransport := connector.transport(0)
	firstChannel := firstTransport.channel(0)
	reconnectResult := make(chan error, 1)
	go func() { reconnectResult <- manager.Reconnect(context.Background(), session.ID) }()
	<-reconnectStarted
	if err := manager.Disconnect(session.ID); err != nil {
		t.Fatal(err)
	}
	close(releaseReconnect)
	if err := <-reconnectResult; err == nil {
		t.Fatal("stale reconnect unexpectedly succeeded")
	}
	if got := session.Info().Status; got != StatusDisconnected {
		t.Fatalf("late reconnect resurrected session: %s", got)
	}
	lateTransport := connector.transport(1)
	if got := lateTransport.closeCount.Load(); got != 1 {
		t.Fatalf("late transport close count = %d", got)
	}
	if got := firstTransport.closeCount.Load(); got != 1 {
		t.Fatalf("first transport close count = %d", got)
	}
	if got := firstChannel.closeCount.Load(); got != 1 {
		t.Fatalf("first PTY close count = %d", got)
	}

	if err := manager.Reconnect(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	if got := session.Info().Status; got != StatusConnected {
		t.Fatalf("explicit reconnect status = %s", got)
	}
	current := connector.transport(2)
	if current.channelCount() != 1 {
		t.Fatalf("replacement PTY count = %d", current.channelCount())
	}
	tab.mu.Lock()
	channel := tab.channel
	generation := tab.generation
	tab.mu.Unlock()
	if channel == nil || generation != session.Info().Generation || channel.Generation() != current.Generation() {
		t.Fatalf("tab was not rebound to current generations: channel=%v tab=%d session=%d transport=%d", channel != nil, generation, session.Info().Generation, current.Generation())
	}
	if err := manager.Write(context.Background(), tab.ID, "client-a", []byte("after")); err != nil {
		t.Fatal(err)
	}
	raw := manager.terminals.(*fakeTerminalFactory).terminal(tab.ID).bytes()
	if got := bytes.Count(raw, reconnectBanner); got != 1 {
		t.Fatalf("reconnect banner count = %d, raw=%q", got, raw)
	}
}

func TestShellExitDoesNotReconnectHealthyConnection(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	transport := connector.transport(0)
	first := openTestTab(t, manager, session, "client-a", "a-1")
	second := openTestTab(t, manager, session, "client-a", "a-2")
	if err := transport.channel(0).Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return first.Info().Exited })
	time.Sleep(20 * time.Millisecond)
	if got := connector.callCount(); got != 1 {
		t.Fatalf("healthy shared connection reconnected after shell exit: %d calls", got)
	}
	if second.Info().Exited {
		t.Fatal("one shell exit killed a sibling tab")
	}
	if got := session.Info().Status; got != StatusConnected {
		t.Fatalf("healthy session status = %s", got)
	}
}

func TestLocalDisconnectReapsAndNetworkDisconnectRetains(t *testing.T) {
	local, _, _, localSession := openTestSession(t, KindLocal)
	openTestTab(t, local, localSession, "client-a", "local-1")
	if err := local.Disconnect(localSession.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Session(localSession.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("local session was retained: %v", err)
	}
	if err := local.Reconnect(context.Background(), localSession.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("local reconnect error = %v", err)
	}

	network, connector, _, networkSession := openTestSession(t, KindSSH)
	openTestTab(t, network, networkSession, "client-a", "ssh-1")
	if err := network.Disconnect(networkSession.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := network.Session(networkSession.ID); err != nil {
		t.Fatalf("network session lost reconnect state: %v", err)
	}
	if got := len(network.ListTabs()); got != 1 {
		t.Fatalf("network disconnect removed %d tabs, want retained tab", 1-got)
	}
	if got := connector.transport(0).closeCount.Load(); got != 1 {
		t.Fatalf("network transport close count = %d", got)
	}
}

func TestIdleSweepRechecksTabStateAndDisconnectsExactlyOnce(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	clock := newSessionTestClock()
	manager.now = clock.Now
	manager.idleTimeout = time.Nanosecond
	session.mu.Lock()
	oldIdle := session.idleSince
	session.mu.Unlock()
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if err := manager.disconnect(session.ID, &oldIdle); err != nil {
		t.Fatal(err)
	}
	if got := session.Info().Status; got != StatusConnected {
		t.Fatalf("stale idle candidate disconnected active tab: %s", got)
	}
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.disconnect(session.ID, &oldIdle); err != nil {
		t.Fatal(err)
	}
	if got := session.Info().Status; got != StatusConnected {
		t.Fatalf("old idle timestamp disconnected new idle period: %s", got)
	}
	clock.Advance(time.Nanosecond)
	if err := manager.SweepIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := session.Info().Status; got != StatusDisconnected {
		t.Fatalf("idle session status = %s", got)
	}
	if err := manager.SweepIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := connector.transport(0).closeCount.Load(); got != 1 {
		t.Fatalf("idle cleanup transport close count = %d", got)
	}
}

func TestExactlyOnceTabAndSessionCleanup(t *testing.T) {
	manager, connector, terminals, session := openTestSession(t, KindLocal)
	transport := connector.transport(0)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := transport.channel(0)
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disconnect(session.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disconnect(session.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if got := channel.closeCount.Load(); got != 1 {
		t.Fatalf("PTY close count = %d", got)
	}
	if got := transport.closeCount.Load(); got != 1 {
		t.Fatalf("transport close count = %d", got)
	}
	if got := terminals.terminal(tab.ID).closes(); got != 1 {
		t.Fatalf("terminal close count = %d", got)
	}
}

func TestWinRMLineModeAndReconnect(t *testing.T) {
	manager, connector, terminals, session := openTestSession(t, KindWinRM)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if _, err := manager.ExecLine(context.Background(), tab.ID, "client-b", "whoami"); !errors.Is(err, ErrNotController) {
		t.Fatalf("WinRM observer exec error = %v", err)
	}
	result, err := manager.ExecLine(context.Background(), tab.ID, "client-a", "whoami")
	if err != nil || result.Stdout != "ran:whoami" {
		t.Fatalf("WinRM exec result = %+v, err = %v", result, err)
	}
	if err := manager.Write(context.Background(), tab.ID, "client-a", []byte("whoami")); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("WinRM PTY write error = %v", err)
	}
	if err := manager.Reconnect(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	if got := connector.transport(1).channelCount(); got != 0 {
		t.Fatalf("WinRM reconnect opened %d PTYs", got)
	}
	raw := terminals.terminal(tab.ID).bytes()
	if !bytes.Contains(raw, []byte("ran:whoami")) || bytes.Count(raw, reconnectBanner) != 1 {
		t.Fatalf("WinRM scrollback/reconnect mismatch: %q", raw)
	}
}

func TestDeadTransportTriggersAutomaticReconnect(t *testing.T) {
	manager, connector, terminals, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	firstTransport := connector.transport(0)
	firstTransport.alive.Store(false)
	if err := firstTransport.channel(0).Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return connector.callCount() == 2 && session.Info().Status == StatusConnected && session.Info().Generation == 2
	})
	waitFor(t, func() bool { return !tab.Info().Exited })
	if got := connector.transport(1).channelCount(); got != 1 {
		t.Fatalf("automatic reconnect opened %d PTYs", got)
	}
	if got := bytes.Count(terminals.terminal(tab.ID).bytes(), reconnectBanner); got != 1 {
		t.Fatalf("automatic reconnect banner count = %d", got)
	}
}

func TestRenameTabEmitsEventAndValidatesTitle(t *testing.T) {
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	var mu sync.Mutex
	var events []Event
	manager := NewManager(Config{
		Connector: connector, Terminals: terminals,
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset-rename", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "a-1")
	mu.Lock()
	events = nil
	mu.Unlock()

	info, err := manager.RenameTab(context.Background(), tab.ID, "部署机")
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "部署机" {
		t.Fatalf("rename info = %+v", info)
	}
	if got := tab.Info().Title; got != "部署机" {
		t.Fatalf("tab info title = %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	var control *ControlEvent
	for index := range events {
		if events[index].Topic == TopicTerminalControl {
			payload, ok := events[index].Payload.(ControlEvent)
			if !ok {
				t.Fatalf("control payload type = %T", events[index].Payload)
			}
			control = &payload
		}
	}
	if control == nil {
		t.Fatal("rename emitted no control event")
	}
	if control.TabID != tab.ID || control.Title != "部署机" {
		t.Fatalf("control event = %+v", control)
	}
	for _, title := range []string{"", "   ", "bad\ttitle", "bad\ntitle", string(make([]byte, 81))} {
		if _, err := manager.RenameTab(context.Background(), tab.ID, title); !errors.Is(err, ErrInvalidTitle) {
			t.Fatalf("title %q: err=%v", title, err)
		}
	}
	if _, err := manager.RenameTab(context.Background(), "missing-tab", "x"); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("missing tab err=%v", err)
	}
}

func TestCreateTabDetachedRouting(t *testing.T) {
	manager, connector, terminals, connected := openTestSession(t, KindSSH)
	provider := newFakeDurableProvider()
	manager.resolver = DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) { return provider, nil })
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{
		SessionID: connected.ID, Title: "构建机", Command: "make -j4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "构建机" || info.Subscribers != 0 {
		t.Fatalf("created info = %+v", info)
	}
	if info.Cols != 80 || info.Rows != 24 {
		t.Fatalf("default size = %dx%d", info.Cols, info.Rows)
	}
	transport := connector.transport(0)
	if transport.channelCount() != 0 || !info.Durable {
		t.Fatalf("pty channels = %d", transport.channelCount())
	}
	if got := provider.attachment(info.ID, 0).written(); !bytes.Equal(got, []byte("make -j4\n")) {
		t.Fatalf("startup command written = %q", got)
	}
	if terminals.terminal(info.ID) == nil {
		t.Fatal("created tab has no terminal")
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1"}); err != nil {
		t.Fatalf("attach after create: %v", err)
	}

	if _, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID, Title: "\t"}); !errors.Is(err, ErrInvalidTitle) {
		t.Fatalf("invalid title err=%v", err)
	}
	if _, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: "missing"}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing session err=%v", err)
	}
}

func TestCreateTabRejectsCommandOnUnsupportedKind(t *testing.T) {
	manager, _, _, connected := openTestSession(t, KindWinRM)
	if _, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID, Command: "ls"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("winrm command = %v; want ErrUnsupported", err)
	}
	if _, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID}); err != nil {
		t.Fatalf("winrm create without command: %v", err)
	}
}

func TestCreateTabRunsCommandOnLocalAndDevice(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		manager, connector, _, connected := openTestSession(t, KindLocal)
		info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID, Cols: 100, Rows: 40, Command: "kimi"})
		if err != nil {
			t.Fatal(err)
		}
		if !info.Ephemeral || info.Cols != 100 || info.Rows != 40 {
			t.Fatalf("local created info = %+v", info)
		}
		if got := connector.transport(0).channel(0).written(); !bytes.Equal(got, []byte("kimi\n")) {
			t.Fatalf("local startup command written = %q", got)
		}
	})
	t.Run("device", func(t *testing.T) {
		provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
		manager := NewManager(Config{
			Connector:       newFakeConnector(),
			Terminals:       newFakeTerminalFactory(),
			DurableResolver: &countingResolver{provider: provider},
		})
		t.Cleanup(func() { _ = manager.Close() })
		connected, err := manager.Connect(context.Background(), Asset{ID: "device/dev-1", Kind: KindDevice})
		if err != nil {
			t.Fatal(err)
		}
		info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID, Command: "kimi"})
		if err != nil {
			t.Fatal(err)
		}
		if !info.Durable {
			t.Fatalf("device created info = %+v", info)
		}
		if got := provider.attachment(info.ID, 0).written(); !bytes.Equal(got, []byte("kimi\n")) {
			t.Fatalf("device startup command written = %q", got)
		}
	})
}

func TestCreateTabLocalAttachDetachReaps(t *testing.T) {
	manager, _, _, connected := openTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-local"}); err != nil {
		t.Fatalf("attach after create: %v", err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tab.Info().Subscribers; got != 1 {
		t.Fatalf("subscribers after attach = %d", got)
	}
	if err := manager.DetachChannel("b-local"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("ephemeral tab after last detach: err=%v", err)
	}
}

func TestChannelReceiverCloseReapsUnretainableEphemeralTab(t *testing.T) {
	manager, connector, _, connected := openTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "receiver-local"}); err != nil {
		t.Fatal(err)
	}
	receiver, err := manager.Bind("receiver-local")
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("ephemeral tab after receiver close: err=%v", err)
	}
	if err := manager.DetachChannel("receiver-local"); err != nil {
		t.Fatal(err)
	}
	if err := receiver.Close(); err != nil {
		t.Fatal(err)
	}
	if got := connector.transport(0).channel(0).closeCount.Load(); got != 1 {
		t.Fatalf("PTY close count = %d, want 1", got)
	}
}

func TestChannelReceiverCloseBeforeAttach(t *testing.T) {
	t.Run("unretainable ephemeral", func(t *testing.T) {
		manager, _, _, connected := openTestSession(t, KindLocal)
		const channelID = "pre-attach-ephemeral"
		info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
		if err != nil {
			t.Fatal(err)
		}
		receiver, err := manager.Bind(channelID)
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: channelID}); !errors.Is(err, hub.ErrDetached) {
			t.Fatalf("attach after receiver close error = %v, want hub.ErrDetached", err)
		}
		tab, err := manager.Tab(info.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := tab.Info().Subscribers; got != 0 {
			t.Fatalf("subscribers after rejected attach = %d", got)
		}
	})

	t.Run("retainable", func(t *testing.T) {
		manager, _, _, connected := openTestSession(t, KindLocal)
		const channelID = "pre-attach-retainable"
		info, err := manager.OpenTab(context.Background(), OpenTabOptions{
			SessionID: connected.ID, Cols: 80, Rows: 24, Detached: true, Ephemeral: true, Retainable: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		receiver, err := manager.Bind(channelID)
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.Close(); err != nil {
			t.Fatal(err)
		}
		attached, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: channelID})
		if err != nil {
			t.Fatal(err)
		}
		if attached.Subscribers != 1 {
			t.Fatalf("retainable attach subscribers = %d", attached.Subscribers)
		}
		rebound, err := manager.Bind(channelID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rebound.Close() })
		if got := manager.Hub().Stats().LiveChannels; got != 1 {
			t.Fatalf("retainable rebind live channels = %d, want 1", got)
		}
	})

	t.Run("raw", func(t *testing.T) {
		manager := NewManager(Config{})
		t.Cleanup(func() { _ = manager.Close() })
		const channelID = "pre-attach-raw"
		receiver, err := manager.Bind(channelID)
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.Close(); err != nil {
			t.Fatal(err)
		}
		producer, err := manager.Hub().Producer(channelID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = producer.Close() })
		rebound, err := manager.Bind(channelID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rebound.Close() })
		data := []byte("raw-after-close")
		if err := producer.SendBinary(context.Background(), data); err != nil {
			t.Fatal(err)
		}
		assertFrameData(t, rebound, data)
	})
}

func TestChannelReceiverCloseRejectedReattachRollback(t *testing.T) {
	tests := []struct {
		name   string
		attach func(*Manager, string, string) error
	}{
		{name: "attach", attach: func(manager *Manager, tabID, channelID string) error {
			_, err := manager.AttachTab(context.Background(), tabID, AttachOptions{ClientID: "client-a", ChannelID: channelID})
			return err
		}},
		{name: "spectate", attach: func(manager *Manager, tabID, channelID string) error {
			_, err := manager.AttachSpectator(context.Background(), tabID, SpectateOptions{ClientID: "client-a", ChannelID: channelID})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, connector, _, connected := openTestSession(t, KindLocal)
			const channelID = "rejected-reattach"
			info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
			if err != nil {
				t.Fatal(err)
			}
			if err := test.attach(manager, info.ID, channelID); err != nil {
				t.Fatal(err)
			}
			receiver, err := manager.Bind(channelID)
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			previous := manager.hooks.channelCloseAfterCapture
			manager.hooks.channelCloseAfterCapture = func(string) {
				close(entered)
				<-release
			}
			var releaseOnce sync.Once
			releaseBarrier := func() {
				releaseOnce.Do(func() { close(release) })
			}
			t.Cleanup(func() {
				releaseBarrier()
				manager.hooks.channelCloseAfterCapture = previous
			})
			closed := make(chan error, 1)
			go func() { closed <- receiver.Close() }()
			<-entered
			if err := test.attach(manager, info.ID, channelID); !errors.Is(err, hub.ErrDetached) {
				t.Fatalf("rejected reattach error = %v, want hub.ErrDetached", err)
			}
			releaseBarrier()
			if err := waitForLifecycleResult(t, closed); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Tab(info.ID); !errors.Is(err, ErrTabNotFound) {
				t.Fatalf("original tab after rejected reattach and close: err=%v", err)
			}
			if got := connector.transport(0).channel(0).closeCount.Load(); got != 1 {
				t.Fatalf("PTY close count = %d, want 1", got)
			}
		})
	}
}

func TestChannelReceiverCloseDoesNotDetachReplacementEphemeralIncarnation(t *testing.T) {
	manager, _, _, connected := openTestSession(t, KindLocal)
	const channelID = "receiver-reused-ephemeral"
	original, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), original.ID, AttachOptions{ClientID: "client-a", ChannelID: channelID}); err != nil {
		t.Fatal(err)
	}
	receiver, err := manager.Bind(channelID)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	previous := manager.hooks.channelCloseAfterCapture
	manager.hooks.channelCloseAfterCapture = func(string) {
		select {
		case <-entered:
			return
		default:
		}
		close(entered)
		<-release
	}
	var releaseOnce sync.Once
	releaseBarrier := func() {
		releaseOnce.Do(func() { close(release) })
	}
	t.Cleanup(func() {
		releaseBarrier()
		manager.hooks.channelCloseAfterCapture = previous
	})
	closed := make(chan error, 1)
	go func() { closed <- receiver.Close() }()
	<-entered
	if err := manager.DetachChannel(channelID); err != nil {
		t.Fatal(err)
	}
	replacement, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), replacement.ID, AttachOptions{ClientID: "client-b", ChannelID: channelID}); err != nil {
		t.Fatal(err)
	}
	replacementReceiver, err := manager.Bind(channelID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replacementReceiver.Close() })
	releaseBarrier()
	if err := waitForLifecycleResult(t, closed); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(original.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("original ephemeral tab after receiver close: err=%v", err)
	}
	tab, err := manager.Tab(replacement.ID)
	if err != nil {
		t.Fatalf("stale receiver close detached replacement ephemeral tab: %v", err)
	}
	if info := tab.Info(); info.Subscribers != 1 || info.Controller != "client-b" {
		t.Fatalf("replacement ephemeral tab state = %+v", info)
	}
	if got := manager.Hub().Stats().LiveChannels; got != 1 {
		t.Fatalf("live channels after stale receiver close = %d, want 1", got)
	}
}

func TestChannelReceiverCloseDoesNotDetachReusedRetainableTab(t *testing.T) {
	manager, _, _, connected := openTestSession(t, KindLocal)
	const channelID = "receiver-reused-retainable"
	original, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), original.ID, AttachOptions{ClientID: "client-a", ChannelID: channelID}); err != nil {
		t.Fatal(err)
	}
	release, closed := pauseChannelReceiverCloseAfterCheck(t, manager, channelID)
	if err := manager.DetachChannel(channelID); err != nil {
		t.Fatal(err)
	}
	replacement, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-b", ChannelID: channelID, Cols: 80, Rows: 24,
		Ephemeral: true, Retainable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := waitForLifecycleResult(t, closed); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(original.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("original ephemeral tab after receiver close: err=%v", err)
	}
	tab, err := manager.Tab(replacement.ID)
	if err != nil {
		t.Fatalf("stale receiver close detached replacement tab: %v", err)
	}
	if info := tab.Info(); info.Subscribers != 1 || info.Controller != "client-b" {
		t.Fatalf("replacement tab state = %+v", info)
	}
}

func TestChannelReceiverCloseDoesNotCloseReusedRawChannel(t *testing.T) {
	manager, _, _, connected := openTestSession(t, KindLocal)
	const channelID = "receiver-reused-raw"
	original, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), original.ID, AttachOptions{ClientID: "client-a", ChannelID: channelID}); err != nil {
		t.Fatal(err)
	}
	release, closed := pauseChannelReceiverCloseAfterCheck(t, manager, channelID)
	if err := manager.DetachChannel(channelID); err != nil {
		t.Fatal(err)
	}
	producer, err := manager.Hub().Producer(channelID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })
	receiver, err := manager.Bind(channelID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	release()
	if err := waitForLifecycleResult(t, closed); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(original.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("original ephemeral tab after receiver close: err=%v", err)
	}
	data := []byte("after-reuse")
	if err := producer.SendBinary(context.Background(), data); err != nil {
		t.Fatalf("stale receiver close closed replacement raw channel: %v", err)
	}
	assertFrameData(t, receiver, data)
}

func TestChannelReceiverCloseDuringAttachDoesNotCloseReusedRetainableTab(t *testing.T) {
	const channelID = "attach-reused-retainable"
	fixture, receiver := newBlockedChannelAttach(t, channelID)
	release, closed := pauseReceiverCloseAfterCheck(t, fixture.manager, receiver)
	if err := fixture.manager.DetachChannel(channelID); err != nil {
		t.Fatal(err)
	}
	if err := waitForLifecycleResult(t, fixture.attached); err == nil {
		t.Fatal("attach succeeded after receiver close")
	}
	replacement, err := fixture.manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: fixture.session.ID, ClientID: "client-b", ChannelID: channelID, Cols: 80, Rows: 24,
		Ephemeral: true, Retainable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	replacementReceiver, err := fixture.manager.Bind(channelID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replacementReceiver.Close() })
	release()
	if err := waitForLifecycleResult(t, closed); err != nil {
		t.Fatal(err)
	}
	tab, err := fixture.manager.Tab(replacement.ID)
	if err != nil {
		t.Fatalf("stale attach cleanup detached replacement tab: %v", err)
	}
	if info := tab.Info(); info.Subscribers != 1 || info.Controller != "client-b" {
		t.Fatalf("replacement tab state = %+v", info)
	}
	if got := fixture.manager.Hub().Stats().LiveChannels; got != 1 {
		t.Fatalf("live channels after stale attach cleanup = %d, want 1", got)
	}
}

func TestChannelReceiverCloseDuringAttachDoesNotCloseReusedRawChannel(t *testing.T) {
	const channelID = "attach-reused-raw"
	fixture, receiver := newBlockedChannelAttach(t, channelID)
	release, closed := pauseReceiverCloseAfterCheck(t, fixture.manager, receiver)
	if err := fixture.manager.DetachChannel(channelID); err != nil {
		t.Fatal(err)
	}
	if err := waitForLifecycleResult(t, fixture.attached); err == nil {
		t.Fatal("attach succeeded after receiver close")
	}
	producer, err := fixture.manager.Hub().Producer(channelID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })
	rawReceiver, err := fixture.manager.Bind(channelID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawReceiver.Close() })
	release()
	if err := waitForLifecycleResult(t, closed); err != nil {
		t.Fatal(err)
	}
	data := []byte("after-attach-reuse")
	if err := producer.SendBinary(context.Background(), data); err != nil {
		t.Fatalf("stale attach cleanup closed replacement raw channel: %v", err)
	}
	assertFrameData(t, rawReceiver, data)
}

func TestChannelDetachCleanupsDoNotDeadlockWithBackpressure(t *testing.T) {
	const backpressureID = "backpressure-cleanup"
	const otherID = "other-cleanup"
	captureStarted := make(chan struct{})
	backpressured := make(chan struct{})
	drainEntered := make(chan struct{})
	var manager *Manager
	bus := hub.New(hub.Options{
		QueueFrames: 1,
		OnBackpressure: func(string, int) {
			close(backpressured)
		},
		OnDrain: func(channelID string, queuedBytes int) {
			if channelID == backpressureID {
				close(drainEntered)
				<-captureStarted
			}
			manager.onDrain(channelID, queuedBytes)
		},
	})
	manager = NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Hub: bus})
	previous := manager.hooks.channelCloseBeforeCapture
	manager.hooks.channelCloseBeforeCapture = func(channelID string) {
		if channelID == otherID {
			close(captureStarted)
		}
	}
	t.Cleanup(func() { manager.hooks.channelCloseBeforeCapture = previous })
	connected, err := manager.Connect(context.Background(), Asset{ID: "cleanup-deadlock", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", backpressureID)
	tab.mu.Lock()
	producer := tab.subscribers[backpressureID].producer
	tab.mu.Unlock()
	if err := producer.SendBinary(context.Background(), []byte("first")); err != nil {
		t.Fatal(err)
	}
	sendResult := make(chan error, 1)
	go func() { sendResult <- producer.SendBinary(context.Background(), []byte("blocked")) }()
	<-backpressured
	if _, err := bus.Producer(otherID); err != nil {
		t.Fatal(err)
	}
	firstCleanup := make(chan error, 1)
	go func() { firstCleanup <- manager.DetachChannel(backpressureID) }()
	<-drainEntered
	secondCleanup := make(chan error, 1)
	go func() { secondCleanup <- manager.DetachChannel(otherID) }()
	if err := waitForLifecycleResult(t, firstCleanup); err != nil {
		t.Fatal(err)
	}
	if err := waitForLifecycleResult(t, secondCleanup); err != nil {
		t.Fatal(err)
	}
	if err := <-sendResult; !errors.Is(err, hub.ErrClosed) {
		t.Fatalf("blocked send error = %v, want hub.ErrClosed", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
}

func pauseChannelReceiverCloseAfterCheck(t *testing.T, manager *Manager, channelID string) (func(), <-chan error) {
	t.Helper()
	receiver, err := manager.Bind(channelID)
	if err != nil {
		t.Fatal(err)
	}
	return pauseReceiverCloseAfterCheck(t, manager, receiver)
}

func pauseReceiverCloseAfterCheck(t *testing.T, manager *Manager, receiver *hub.Receiver) (func(), <-chan error) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	previous := manager.hooks.channelCloseAfterCheck
	manager.hooks.channelCloseAfterCheck = func() {
		close(entered)
		<-release
	}
	var releaseOnce sync.Once
	releaseBarrier := func() {
		releaseOnce.Do(func() { close(release) })
	}
	t.Cleanup(func() {
		releaseBarrier()
		manager.hooks.channelCloseAfterCheck = previous
	})
	closed := make(chan error, 1)
	go func() { closed <- receiver.Close() }()
	<-entered
	return releaseBarrier, closed
}

type blockedChannelAttachFixture struct {
	manager  *Manager
	session  *Session
	attached chan error
}

func newBlockedChannelAttach(t *testing.T, channelID string) (*blockedChannelAttachFixture, *hub.Receiver) {
	t.Helper()
	var manager *Manager
	bus := hub.New(hub.Options{
		QueueFrames: 1,
		OnChannelClose: func(channelID string, incarnation uint64, closeChannel func()) {
			manager.onChannelClose(channelID, incarnation, closeChannel)
		},
	})
	t.Cleanup(func() { _ = bus.Close() })
	manager = NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Hub: bus})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "blocked-attach", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	tab.terminal.Feed(make([]byte, 256<<10))
	receiver, err := manager.Bind(channelID)
	if err != nil {
		t.Fatal(err)
	}
	attached := make(chan error, 1)
	go func() {
		_, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{
			ClientID: "client-a", ChannelID: channelID, ReplayBytes: -1,
		})
		attached <- err
	}()
	waitFor(t, func() bool { return bus.Stats().QueuedFrames == 1 })
	return &blockedChannelAttachFixture{manager: manager, session: connected, attached: attached}, receiver
}

func TestCreateTabDeviceDurableAttach(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	manager := NewManager(Config{
		Connector:       newFakeConnector(),
		Terminals:       newFakeTerminalFactory(),
		DurableResolver: &countingResolver{provider: provider},
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "device/dev-1", Kind: KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !info.Durable || info.Ephemeral || info.Subscribers != 0 {
		t.Fatalf("device created info = %+v", info)
	}
	if provider.createCalls != 1 {
		t.Fatalf("provider create calls = %d; want 1", provider.createCalls)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-device"}); err != nil {
		t.Fatalf("attach after create: %v", err)
	}
	if err := manager.DetachChannel("b-device"); err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatalf("durable tab must survive detach: %v", err)
	}
	if got := tab.Info().Subscribers; got != 0 {
		t.Fatalf("subscribers after detach = %d", got)
	}
}

type shellTestTransport struct {
	*fakeTransport
	shell string
}

func (t *shellTestTransport) Shell() string { return t.shell }

func TestTabShellPreservedAcrossAttach(t *testing.T) {
	transport := &shellTestTransport{fakeTransport: newFakeTransport(KindLocal, 1), shell: "powershell"}
	manager := NewManager(Config{
		Connector: ConnectorFunc(func(context.Context, Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
		Terminals: newFakeTerminalFactory(),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "local-shell", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "shell-a", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Shell != ShellPowerShell {
		t.Fatalf("created shell = %q, want powershell", info.Shell)
	}
	transport.shell = "cmd"
	attached, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "shell-b"})
	if err != nil {
		t.Fatal(err)
	}
	if attached.Shell != ShellPowerShell {
		t.Fatalf("reattached shell = %q, want creation classification preserved", attached.Shell)
	}
}

func TestInteractiveTabShellDefaultsUnknown(t *testing.T) {
	for _, kind := range []string{KindSSH, KindDocker} {
		t.Run(kind, func(t *testing.T) {
			manager, _, _, connected := openTestSession(t, kind)
			info := openTestTab(t, manager, connected, "client-a", "unknown-shell").Info()
			if info.Shell != ShellUnknown {
				t.Fatalf("shell = %q, want unknown", info.Shell)
			}
		})
	}
}

func TestDurableRecoveryShellUnknown(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	tabID := "0123456789abcdef0123456789abcdef"
	if _, err := provider.Create(context.Background(), base.DurableCreateOptions{ID: tabID, Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{
		Connector:       newFakeConnector(),
		Terminals:       newFakeTerminalFactory(),
		DurableResolver: &countingResolver{provider: provider},
	})
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.Connect(context.Background(), Asset{ID: "ssh-recovery", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	info, err := manager.RecoverRemoteDurable(context.Background(), tabID, OpenTabOptions{
		ClientID: "client-a", ChannelID: "recover-shell", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Shell != ShellUnknown {
		t.Fatalf("recovered shell = %q, want unknown", info.Shell)
	}
}

func openTestSession(t *testing.T, kind string) (*Manager, *fakeConnector, *fakeTerminalFactory, *Session) {
	t.Helper()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{
		Connector: connector, Terminals: terminals, ReconnectMax: 2,
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset-" + kind, Name: kind, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return manager, connector, terminals, session
}

func openTestTab(t *testing.T, manager *Manager, session *Session, client, channel string) *Tab {
	t.Helper()
	info, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: session.ID, ClientID: client, ChannelID: channel, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func bindTestReceiver(t *testing.T, manager *Manager, channel string) *hub.Receiver {
	t.Helper()
	receiver, err := manager.Bind(channel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	return receiver
}

func receiveTestFrame(t *testing.T, receiver *hub.Receiver) hub.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := receiver.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Ack(frame.Sequence); err != nil {
		t.Fatal(err)
	}
	return frame
}

func assertFrameData(t *testing.T, receiver *hub.Receiver, expected []byte) {
	t.Helper()
	frame := receiveTestFrame(t, receiver)
	if !bytes.Equal(frame.Data, expected) {
		t.Fatalf("frame = %v %q, want %q", frame.Kind, frame.Data, expected)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestConcurrentClientsCanWriteIndependentTabs(t *testing.T) {
	manager, _, _, session := openTestSession(t, KindSSH)
	const tabs = 8
	var wg sync.WaitGroup
	for index := 0; index < tabs; index++ {
		tab := openTestTab(t, manager, session, fmt.Sprintf("client-%d", index), fmt.Sprintf("channel-%d", index))
		wg.Add(1)
		go func() {
			defer wg.Done()
			for write := 0; write < 100; write++ {
				if err := manager.Write(context.Background(), tab.ID, tab.Info().Controller, []byte("x")); err != nil {
					t.Errorf("write %s: %v", tab.ID, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestDeviceSessionOwnerEnforcedAcrossUsers(t *testing.T) {
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	asset := Asset{ID: "device/d-1", Kind: KindDevice}
	alice := ipc.WithUserID(context.Background(), "user-a")
	bob := ipc.WithUserID(context.Background(), "user-b")

	session, err := manager.Connect(alice, asset)
	if err != nil {
		t.Fatal(err)
	}
	other, err := manager.Connect(bob, asset)
	if err != nil {
		t.Fatalf("second owner connect = %v", err)
	}
	if other == session {
		t.Fatal("second owner reused the first owner's device session")
	}
	if reused, err := manager.Connect(alice, asset); err != nil || reused != session {
		t.Fatalf("owner re-connect = %p, %v", reused, err)
	}
	if _, err := manager.OpenTab(bob, OpenTabOptions{SessionID: session.ID, ClientID: "bob", ChannelID: "ch-b", Cols: 80, Rows: 24}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-user open tab = %v, want ErrForbidden", err)
	}
	tab := openTestTab(t, manager, session, "alice", "ch-a")
	if _, err := manager.AttachTab(bob, tab.ID, AttachOptions{ClientID: "bob", ChannelID: "ch-b2"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-user attach = %v, want ErrForbidden", err)
	}
	if err := manager.Reconnect(bob, session.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-user reconnect = %v, want ErrForbidden", err)
	}
}

func TestCreateTabRejectsRemoteKind(t *testing.T) {
	// A remote shell carries no backend channel (a preview feed, not a shell), so creation must refuse cleanly
	// instead of panicking in OpenTab.
	manager, _, _, session := openTestSession(t, KindSSH)
	session.mu.Lock()
	session.asset.Kind = KindRemote
	session.mu.Unlock()
	if _, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: session.ID}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("CreateTab on remote = %v; want ErrUnsupported", err)
	}
	if _, err := manager.OpenTab(context.Background(), OpenTabOptions{SessionID: session.ID, ChannelID: "c-1", Cols: 80, Rows: 24}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("OpenTab on remote = %v; want ErrUnsupported", err)
	}
}

func requestTestUser(userID string) context.Context {
	return ipc.WithUserID(context.Background(), userID)
}

func requestTestAdmin(userID string) context.Context {
	return ipc.WithRole(requestTestUser(userID), "superadmin")
}

func assertRequestResult(t *testing.T, err error, allowed bool) {
	t.Helper()
	if allowed && err != nil {
		t.Fatalf("requester was denied: %v", err)
	}
	if !allowed && !errors.Is(err, ErrForbidden) {
		t.Fatalf("requester error = %v, want ErrForbidden", err)
	}
}

func TestRequestListAndLookupFilterBySessionOwner(t *testing.T) {
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	ownerA := requestTestUser("owner-a")
	ownerB := requestTestUser("owner-b")
	open := WithOpenAccess(context.Background())

	sessionA, err := manager.Connect(ownerA, Asset{ID: "asset-a", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tabA := openTestTab(t, manager, sessionA, "client-a", "channel-a")
	sessionB, err := manager.Connect(ownerB, Asset{ID: "asset-b", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tabB := openTestTab(t, manager, sessionB, "client-b", "channel-b")
	sessionOpen, err := manager.Connect(open, Asset{ID: "asset-open", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tabOpen := openTestTab(t, manager, sessionOpen, "client-open", "channel-open")

	targets := []struct {
		name    string
		session *Session
		tab     *Tab
	}{
		{name: "a", session: sessionA, tab: tabA},
		{name: "b", session: sessionB, tab: tabB},
		{name: "ownerless", session: sessionOpen, tab: tabOpen},
	}
	actors := []struct {
		name   string
		ctx    context.Context
		access map[string]bool
		closed bool
	}{
		{name: "owner-a", ctx: ownerA, access: map[string]bool{"a": true}},
		{name: "owner-b", ctx: ownerB, access: map[string]bool{"b": true}},
		{name: "superadmin", ctx: requestTestAdmin("admin"), access: map[string]bool{}},
		{name: "open", ctx: open, access: map[string]bool{}, closed: true},
		{name: "closed", ctx: context.Background(), access: map[string]bool{}, closed: true},
		{name: "same-account-second-client", ctx: requestTestUser("owner-a"), access: map[string]bool{"a": true}},
	}
	for _, actor := range actors {
		t.Run(actor.name, func(t *testing.T) {
			wantSessions := make(map[string]bool)
			wantTabs := make(map[string]bool)
			for _, target := range targets {
				if actor.access[target.name] {
					wantSessions[target.session.ID] = true
					wantTabs[target.tab.ID] = true
				}
			}
			sessions, err := manager.ListSessionsFor(actor.ctx)
			if actor.closed {
				assertRequestResult(t, err, false)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(sessions) != len(wantSessions) {
					t.Fatalf("visible sessions = %+v, want IDs %+v", sessions, wantSessions)
				}
				for _, info := range sessions {
					if !wantSessions[info.ID] {
						t.Fatalf("visible session %q was not authorized", info.ID)
					}
				}
			}
			tabs, err := manager.ListTabsFor(actor.ctx)
			if actor.closed {
				assertRequestResult(t, err, false)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(tabs) != len(wantTabs) {
					t.Fatalf("visible tabs = %+v, want IDs %+v", tabs, wantTabs)
				}
				for _, info := range tabs {
					if !wantTabs[info.ID] {
						t.Fatalf("visible tab %q was not authorized", info.ID)
					}
				}
			}
			for _, target := range targets {
				allowed := actor.access[target.name]
				t.Run("lookup-"+target.name, func(t *testing.T) {
					_, err := manager.SessionFor(actor.ctx, target.session.ID)
					assertRequestResult(t, err, allowed)
					_, err = manager.TabFor(actor.ctx, target.tab.ID)
					assertRequestResult(t, err, allowed)
				})
			}
		})
	}

	forged := &Tab{ID: "forged-parent", SessionID: sessionB.ID, session: sessionA}
	manager.mu.Lock()
	manager.tabs[forged.ID] = forged
	manager.mu.Unlock()
	if _, err := manager.TabFor(ownerA, forged.ID); err != nil {
		t.Fatalf("tab was not authorized by its parent session: %v", err)
	}
	if _, err := manager.TabFor(ownerB, forged.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("tab SessionID overrode parent ownership: %v", err)
	}
	manager.mu.Lock()
	delete(manager.tabs, forged.ID)
	manager.mu.Unlock()

	if _, err := manager.SessionFor(ownerA, "missing-session"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing session error = %v", err)
	}
	if _, err := manager.TabFor(ownerA, "missing-tab"); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("missing tab error = %v", err)
	}
	if got := len(manager.ListSessions()); got != len(targets) {
		t.Fatalf("trusted session list length = %d, want %d", got, len(targets))
	}
	if got := len(manager.ListTabs()); got != len(targets) {
		t.Fatalf("trusted tab list length = %d, want %d", got, len(targets))
	}
}

func TestRequestOpenAccessConfiguration(t *testing.T) {
	newManager := func(t *testing.T, open bool) *Manager {
		t.Helper()
		manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), OpenAccess: open})
		t.Cleanup(func() { _ = manager.Close() })
		if _, err := manager.Connect(requestTestUser("owner-a"), Asset{ID: "asset", Kind: KindSSH}); err != nil {
			t.Fatal(err)
		}
		return manager
	}

	closed := newManager(t, false)
	if _, err := closed.ListSessionsFor(context.Background()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("closed list error = %v, want ErrForbidden", err)
	}
	if _, err := closed.SessionFor(context.Background(), closed.ListSessions()[0].ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("closed lookup error = %v, want ErrForbidden", err)
	}
	if _, err := closed.SessionFor(context.Background(), "missing-session"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("closed missing session error = %v, want ErrForbidden", err)
	}
	if _, err := closed.TabFor(context.Background(), "missing-tab"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("closed missing tab error = %v, want ErrForbidden", err)
	}
	if err := closed.CloseTabFor(context.Background(), "missing-tab"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("closed close error = %v, want ErrForbidden", err)
	}
	if infos, err := closed.ListSessionsFor(WithOpenAccess(context.Background())); !errors.Is(err, ErrForbidden) || infos != nil {
		t.Fatalf("explicit marker list on closed server = %+v, %v, want ErrForbidden", infos, err)
	}

	open := newManager(t, true)
	if infos, err := open.ListSessionsFor(context.Background()); err != nil || len(infos) != 1 {
		t.Fatalf("configured open list = %+v, %v", infos, err)
	}
	if _, err := open.SessionFor(context.Background(), open.ListSessions()[0].ID); err != nil {
		t.Fatalf("configured open lookup: %v", err)
	}
}

func TestRequestOpenAccessIdentityRequirement(t *testing.T) {
	newManager := func(t *testing.T, open bool) *Manager {
		t.Helper()
		manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), OpenAccess: open})
		t.Cleanup(func() { _ = manager.Close() })
		if _, err := manager.Connect(requestTestUser("owner-a"), Asset{ID: "asset", Kind: KindSSH}); err != nil {
			t.Fatal(err)
		}
		return manager
	}

	// A non-open server rejects identity-less open access even with the
	// context flag: the flag is an in-process vouch, not an authorization.
	closed := newManager(t, false)
	sessionID := closed.ListSessions()[0].ID
	marker := WithOpenAccess(context.Background())
	if _, err := closed.SessionFor(marker, sessionID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("identity-less marker session lookup = %v, want ErrForbidden", err)
	}
	if _, err := closed.TabFor(marker, "any-tab"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("identity-less marker tab lookup = %v, want ErrForbidden", err)
	}
	if err := closed.CloseTabFor(marker, "any-tab"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("identity-less marker close = %v, want ErrForbidden", err)
	}
	if _, err := closed.ConnectFor(marker, Asset{ID: "other", Kind: KindSSH}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("identity-less marker connect = %v, want ErrForbidden", err)
	}

	// An authenticated identity is not gated by the open-access boundary:
	// the owner reaches their own resources without any flag.
	owner := requestTestUser("owner-a")
	if _, err := closed.SessionFor(owner, sessionID); err != nil {
		t.Fatalf("owner session lookup: %v", err)
	}
	if infos, err := closed.ListSessionsFor(owner); err != nil || len(infos) != 1 {
		t.Fatalf("owner session list = %+v, %v", infos, err)
	}
	// An identity backed by the in-process vouch keeps open-access semantics.
	if infos, err := closed.ListSessionsFor(WithOpenAccess(owner)); err != nil || len(infos) != 1 {
		t.Fatalf("vouched identity session list = %+v, %v", infos, err)
	}

	// An explicitly open deployment (and desktop, which sets the same config)
	// keeps accepting identity-less requests with or without the flag.
	open := newManager(t, true)
	if _, err := open.SessionFor(context.Background(), open.ListSessions()[0].ID); err != nil {
		t.Fatalf("open deployment identity-less lookup: %v", err)
	}
	if _, err := open.SessionFor(marker, open.ListSessions()[0].ID); err != nil {
		t.Fatalf("open deployment marker lookup: %v", err)
	}
}

func TestConnectForAccessScope(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	asset := Asset{ID: "asset", Kind: KindSSH}
	if _, err := manager.ConnectFor(context.Background(), asset); !errors.Is(err, ErrForbidden) {
		t.Fatalf("closed connect error = %v, want ErrForbidden", err)
	}
	if got := connector.callCount(); got != 0 {
		t.Fatalf("closed connect called connector %d times", got)
	}
	if _, err := manager.ConnectFor(WithOpenAccess(context.Background()), asset); !errors.Is(err, ErrForbidden) {
		t.Fatalf("marker connect on closed server = %v, want ErrForbidden", err)
	}
	openManager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), OpenAccess: true})
	t.Cleanup(func() { _ = openManager.Close() })
	if _, err := openManager.ConnectFor(WithOpenAccess(context.Background()), asset); err != nil {
		t.Fatalf("open deployment marker connect: %v", err)
	}
	if _, err := openManager.ConnectFor(context.Background(), asset); err != nil {
		t.Fatalf("open deployment connect: %v", err)
	}
	if _, err := manager.ConnectFor(requestTestUser("owner-a"), Asset{ID: "owned", Kind: KindSSH}); err != nil {
		t.Fatalf("account connect: %v", err)
	}
}

func TestConnectScopesAssetReuseByOwner(t *testing.T) {
	for _, kind := range []string{KindSSH, KindLocal, KindDocker, KindWinRM, KindDevice} {
		t.Run(kind, func(t *testing.T) {
			connector := newFakeConnector()
			manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), ReconnectBackoff: []time.Duration{0}})
			t.Cleanup(func() { _ = manager.Close() })
			ownerA := requestTestUser("owner-a")
			ownerB := requestTestUser("owner-b")
			asset := Asset{ID: "shared-asset", Kind: kind}
			first, err := manager.Connect(ownerA, asset)
			if err != nil {
				t.Fatal(err)
			}
			second, err := manager.Connect(ownerB, asset)
			if err != nil {
				t.Fatalf("second owner connect = %v", err)
			}
			if first == second {
				t.Fatal("two owners share one session for the same asset ID")
			}
			if got := connector.callCount(); got != 2 {
				t.Fatalf("connector calls = %d, want 2", got)
			}
			if first.Asset().ID != asset.ID || first.Info().AssetID != asset.ID {
				t.Fatalf("asset ID was changed by owner scoping: asset=%q info=%q", first.Asset().ID, first.Info().AssetID)
			}
			if reused, err := manager.Connect(requestTestUser("owner-a"), asset); err != nil || reused != first {
				t.Fatalf("first owner reuse = %p, %v", reused, err)
			}
			if reused, err := manager.Connect(ownerB, asset); err != nil || reused != second {
				t.Fatalf("second owner reuse = %p, %v", reused, err)
			}
			wantCalls := 2
			if kind != KindLocal {
				if err := manager.Reconnect(ownerA, first.ID); err != nil {
					t.Fatalf("first owner reconnect = %v", err)
				}
				if err := manager.Reconnect(ownerB, second.ID); err != nil {
					t.Fatalf("second owner reconnect = %v", err)
				}
				wantCalls += 2
			}
			if got := connector.callCount(); got != wantCalls {
				t.Fatalf("connector calls after reconnect = %d, want %d", got, wantCalls)
			}
			if err := manager.Disconnect(first.ID); err != nil {
				t.Fatal(err)
			}
			if reused, err := manager.Connect(ownerB, asset); err != nil || reused != second {
				t.Fatalf("second owner was affected by first disconnect: %p, %v", reused, err)
			}
			if got := connector.callCount(); got != wantCalls {
				t.Fatalf("second owner reconnect after peer disconnect = %d, want %d", got, wantCalls)
			}
			replacement, err := manager.Connect(ownerA, asset)
			if err != nil {
				t.Fatal(err)
			}
			if replacement == first || replacement == second {
				t.Fatal("disconnect cleanup reused the wrong owner scope")
			}
			if got := connector.callCount(); got != wantCalls+1 {
				t.Fatalf("connector calls after owner-scoped cleanup = %d, want %d", got, wantCalls+1)
			}
		})
	}
}

func TestConnectDoesNotReuseOwnerlessSessionForAccount(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	open := WithOpenAccess(context.Background())
	ownerA := requestTestUser("owner-a")
	asset := Asset{ID: "shared-asset", Kind: KindSSH}
	openSession, err := manager.Connect(open, asset)
	if err != nil {
		t.Fatal(err)
	}
	ownedSession, err := manager.Connect(ownerA, asset)
	if err != nil {
		t.Fatal(err)
	}
	if ownedSession == openSession {
		t.Fatal("authenticated request reused an ownerless session")
	}
	if got := connector.callCount(); got != 2 {
		t.Fatalf("connector calls = %d, want 2", got)
	}
	if reused, err := manager.Connect(open, asset); err != nil || reused != openSession {
		t.Fatalf("open reuse = %p, %v", reused, err)
	}
	if reused, err := manager.Connect(ownerA, asset); err != nil || reused != ownedSession {
		t.Fatalf("account reuse = %p, %v", reused, err)
	}
	if got := connector.callCount(); got != 2 {
		t.Fatalf("connector calls after owner-scoped reuse = %d, want 2", got)
	}
}

func TestConnectFailureCleanupIsOwnerScoped(t *testing.T) {
	connector := newFakeConnector()
	connectBoom := errors.New("connect failed")
	connector.connect = func(_ context.Context, asset Asset, _ uint64, call int) (base.Transport, error) {
		if call == 2 {
			return nil, connectBoom
		}
		return newFakeTransport(asset.Kind, uint64(call)+1000), nil
	}
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	ownerA := requestTestUser("owner-a")
	ownerB := requestTestUser("owner-b")
	asset := Asset{ID: "shared-asset", Kind: KindSSH}
	first, err := manager.Connect(ownerA, asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Connect(ownerB, asset); !errors.Is(err, connectBoom) {
		t.Fatalf("second owner connect error = %v, want %v", err, connectBoom)
	}
	if reused, err := manager.Connect(ownerA, asset); err != nil || reused != first {
		t.Fatalf("first owner after peer failure = %p, %v", reused, err)
	}
	second, err := manager.Connect(ownerB, asset)
	if err != nil {
		t.Fatalf("second owner retry = %v", err)
	}
	if second == first {
		t.Fatal("failed owner scope reused the first owner's session")
	}
	if reused, err := manager.Connect(ownerA, asset); err != nil || reused != first {
		t.Fatalf("first owner after peer retry = %p, %v", reused, err)
	}
	if got := connector.callCount(); got != 3 {
		t.Fatalf("connector calls = %d, want 3", got)
	}
}
