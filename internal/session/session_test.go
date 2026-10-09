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
	time.Sleep(time.Millisecond)
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
