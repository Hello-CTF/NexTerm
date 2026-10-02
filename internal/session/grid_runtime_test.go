package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/terminalgrid"
)

func openGridTestSession(t *testing.T, events chan ControlEvent) (*Manager, *fakeConnector, *fakeTerminalFactory, *Session) {
	t.Helper()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{
		Connector: connector,
		Terminals: terminals,
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			if event.Topic == TopicTerminalControl {
				events <- event.Payload.(ControlEvent)
			}
			return nil
		}),
		ReconnectMax:     2,
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "grid-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	return manager, connector, terminals, session
}

func resizeAsync(manager *Manager, tabID, client string, cols, rows uint32) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- manager.Resize(context.Background(), tabID, client, cols, rows)
	}()
	return result
}

func fakeChannelGrid(channel *fakeChannel) (int, uint32, uint32) {
	channel.mu.Lock()
	defer channel.mu.Unlock()
	return channel.resizes, channel.cols, channel.rows
}

func fakeTerminalGrid(terminal *fakeTerminal) (int, int) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.cols, terminal.rows
}

func committedGridEvents(events chan ControlEvent) []ControlEvent {
	var committed []ControlEvent
	for {
		select {
		case event := <-events:
			if event.GridRevision > 0 {
				committed = append(committed, event)
			}
		default:
			return committed
		}
	}
}

func TestGridRuntimeSerializedLatestWinsAndPropagates(t *testing.T) {
	events := make(chan ControlEvent, 32)
	manager, connector, terminals, session := openGridTestSession(t, events)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1"}); err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	channel.resizeHook = func(ctx context.Context, cols, rows uint32) error {
		if cols == 90 {
			once.Do(func() { close(started) })
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}

	first := resizeAsync(manager, tab.ID, "client-a", 90, 30)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first resize did not reach the transport")
	}
	second := resizeAsync(manager, tab.ID, "client-a", 100, 40)
	waitFor(t, func() bool {
		snapshot, err := manager.GridSnapshot(tab.ID)
		return err == nil && snapshot.Pending && snapshot.DesiredGrid == (terminalgrid.Grid{Cols: 100, Rows: 40})
	})
	third := resizeAsync(manager, tab.ID, "client-a", 110, 50)
	waitFor(t, func() bool {
		snapshot, err := manager.GridSnapshot(tab.ID)
		return err == nil && snapshot.DesiredGrid == (terminalgrid.Grid{Cols: 110, Rows: 50})
	})
	close(release)
	for index, result := range []<-chan error{first, second, third} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("resize %d error = %v", index, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("resize %d did not complete", index)
		}
	}

	if calls, cols, rows := fakeChannelGrid(channel); calls != 2 || cols != 110 || rows != 50 {
		t.Fatalf("transport grid = calls %d, %dx%d", calls, cols, rows)
	}
	if cols, rows := fakeTerminalGrid(terminals.terminal(tab.ID)); cols != 110 || rows != 50 {
		t.Fatalf("terminal grid = %dx%d", cols, rows)
	}
	info := tab.Info()
	if info.Cols != 110 || info.Rows != 50 || info.GridRevision != 2 {
		t.Fatalf("tab grid info = %+v", info)
	}
	snapshot, err := manager.GridSnapshot(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DesiredGrid != (terminalgrid.Grid{Cols: 110, Rows: 50}) || snapshot.CommittedGrid != snapshot.DesiredGrid || snapshot.InFlight || snapshot.Pending {
		t.Fatalf("final grid snapshot = %+v", snapshot)
	}
	committed := committedGridEvents(events)
	if len(committed) != 2 {
		t.Fatalf("committed control events = %+v", committed)
	}
	if committed[0].Cols != 90 || committed[0].Rows != 30 || committed[0].GridRevision != 1 || committed[1].Cols != 110 || committed[1].Rows != 50 || committed[1].GridRevision != 2 {
		t.Fatalf("committed grid sequence = %+v", committed)
	}
	for _, event := range committed {
		if event.Subscribers != 2 || event.Viewers != 2 {
			t.Fatalf("grid event did not describe both clients: %+v", event)
		}
	}
	if err := manager.Resize(context.Background(), tab.ID, "client-b", 70, 20); !errors.Is(err, ErrNotController) {
		t.Fatalf("observer resize error = %v", err)
	}
}

func TestGridRuntimeTransportFailureDoesNotCommit(t *testing.T) {
	events := make(chan ControlEvent, 16)
	manager, connector, terminals, session := openGridTestSession(t, events)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	transportErr := errors.New("window change rejected")
	channel.resizeHook = func(_ context.Context, cols, _ uint32) error {
		if cols == 100 {
			return transportErr
		}
		return nil
	}
	if err := manager.Resize(context.Background(), tab.ID, "client-a", 100, 40); !errors.Is(err, transportErr) {
		t.Fatalf("resize error = %v", err)
	}
	info := tab.Info()
	if info.Cols != 80 || info.Rows != 24 || info.GridRevision != 0 {
		t.Fatalf("failed resize changed tab state: %+v", info)
	}
	if cols, rows := fakeTerminalGrid(terminals.terminal(tab.ID)); cols != 80 || rows != 24 {
		t.Fatalf("failed resize changed terminal to %dx%d", cols, rows)
	}
	snapshot, err := manager.GridSnapshot(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	initial := terminalgrid.Grid{Cols: 80, Rows: 24}
	if snapshot.CommittedGrid != initial || snapshot.LastGoodGrid != initial {
		t.Fatalf("failed resize changed known-good state: %+v", snapshot)
	}
	if got := committedGridEvents(events); len(got) != 0 {
		t.Fatalf("failed resize emitted commits: %+v", got)
	}
	if err := manager.Resize(context.Background(), tab.ID, "client-a", 110, 45); err != nil {
		t.Fatal(err)
	}
	if got := tab.Info(); got.Cols != 110 || got.Rows != 45 || got.GridRevision != 1 {
		t.Fatalf("recovery grid = %+v", got)
	}
}

func TestGridRuntimeReconnectFencesDelayedOldTransport(t *testing.T) {
	events := make(chan ControlEvent, 32)
	manager, connector, terminals, session := openGridTestSession(t, events)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	oldChannel := connector.transport(0).channel(0)
	started := make(chan struct{})
	release := make(chan struct{})
	oldChannel.resizeHook = func(_ context.Context, cols, _ uint32) error {
		if cols == 95 {
			close(started)
			<-release
		}
		return nil
	}
	oldResult := resizeAsync(manager, tab.ID, "client-a", 95, 35)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("old transport resize did not start")
	}
	reconnectResult := make(chan error, 1)
	go func() { reconnectResult <- manager.Reconnect(context.Background(), session.ID) }()
	waitFor(t, func() bool {
		return session.Info().Status == StatusConnected && connector.callCount() == 2 && connector.transport(1).channelCount() == 1
	})
	newChannel := connector.transport(1).channel(0)
	if calls, _, _ := fakeChannelGrid(newChannel); calls != 0 {
		t.Fatalf("replacement resized before old completion: %d", calls)
	}
	latestResult := resizeAsync(manager, tab.ID, "client-a", 120, 40)
	waitFor(t, func() bool {
		snapshot, err := manager.GridSnapshot(tab.ID)
		return err == nil && snapshot.DesiredGrid == (terminalgrid.Grid{Cols: 120, Rows: 40})
	})
	close(release)
	if err := <-oldResult; err != nil && !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("delayed old resize error = %v", err)
	}
	if err := <-latestResult; err != nil {
		t.Fatal(err)
	}
	if err := <-reconnectResult; err != nil {
		t.Fatal(err)
	}
	if calls, cols, rows := fakeChannelGrid(newChannel); calls != 1 || cols != 120 || rows != 40 {
		t.Fatalf("replacement grid = calls %d, %dx%d", calls, cols, rows)
	}
	if cols, rows := fakeTerminalGrid(terminals.terminal(tab.ID)); cols != 120 || rows != 40 {
		t.Fatalf("terminal after reconnect = %dx%d", cols, rows)
	}
	if info := tab.Info(); info.GridRevision != 1 || info.Cols != 120 || info.Rows != 40 {
		t.Fatalf("old completion changed revisioned grid state: %+v", info)
	}
	snapshot, err := manager.GridSnapshot(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CommittedGrid != (terminalgrid.Grid{Cols: 120, Rows: 40}) || snapshot.InFlight || snapshot.Pending {
		t.Fatalf("reconnected snapshot = %+v", snapshot)
	}
}

func TestGridRuntimeClaimFencesDelayedResize(t *testing.T) {
	events := make(chan ControlEvent, 32)
	manager, connector, terminals, session := openGridTestSession(t, events)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1"}); err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	started := make(chan struct{})
	release := make(chan struct{})
	channel.resizeHook = func(_ context.Context, cols, _ uint32) error {
		if cols == 90 {
			close(started)
			<-release
		}
		return nil
	}
	oldResult := resizeAsync(manager, tab.ID, "client-a", 90, 30)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("pre-claim resize did not start")
	}
	if previous, err := manager.Claim(tab.ID, "client-b"); err != nil || previous != "client-a" {
		t.Fatalf("claim previous = %q, err = %v", previous, err)
	}
	latestResult := resizeAsync(manager, tab.ID, "client-b", 120, 40)
	waitFor(t, func() bool {
		snapshot, err := manager.GridSnapshot(tab.ID)
		return err == nil && snapshot.DesiredGrid == (terminalgrid.Grid{Cols: 120, Rows: 40})
	})
	close(release)
	if err := <-oldResult; err != nil && !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("pre-claim resize error = %v", err)
	}
	if err := <-latestResult; err != nil {
		t.Fatal(err)
	}
	if calls, cols, rows := fakeChannelGrid(channel); calls != 2 || cols != 120 || rows != 40 {
		t.Fatalf("claimed transport grid = calls %d, %dx%d", calls, cols, rows)
	}
	if cols, rows := fakeTerminalGrid(terminals.terminal(tab.ID)); cols != 120 || rows != 40 {
		t.Fatalf("claimed terminal grid = %dx%d", cols, rows)
	}
	if info := tab.Info(); info.Controller != "client-b" || info.GridRevision != 1 {
		t.Fatalf("claim committed stale grid: %+v", info)
	}
}

func TestGridRuntimeHiddenResumeAndFinalFlush(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, ReconnectBackoff: []time.Duration{0}})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "grid-hidden", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	if err := manager.SetVisible(tab.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.Resize(context.Background(), tab.ID, "client-a", 91, 27); !errors.Is(err, ErrHidden) {
		t.Fatalf("hidden resize error = %v", err)
	}
	if calls, _, _ := fakeChannelGrid(channel); calls != 0 {
		t.Fatalf("hidden resize reached transport %d times", calls)
	}
	snapshot, err := manager.GridSnapshot(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	desired := terminalgrid.Grid{Cols: 91, Rows: 27}
	if snapshot.Mode != terminalgrid.ModeHidden || snapshot.DesiredGrid != desired || snapshot.CommittedGrid != (terminalgrid.Grid{Cols: 80, Rows: 24}) {
		t.Fatalf("hidden snapshot = %+v", snapshot)
	}
	if err := manager.SetVisible(tab.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.FlushResize(context.Background(), tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = manager.GridSnapshot(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CommittedGrid != desired || snapshot.LastGoodGrid != desired {
		t.Fatalf("resumed snapshot = %+v", snapshot)
	}
	terminalSnapshot, err := manager.TerminalSnapshot(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if terminalSnapshot.Cols != 91 || terminalSnapshot.Rows != 27 {
		t.Fatalf("resumed terminal = %+v", terminalSnapshot)
	}
	beforeCalls, _, _ := fakeChannelGrid(channel)
	beforeRevision := tab.Info().GridRevision
	if err := manager.FlushResize(context.Background(), tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if afterCalls, _, _ := fakeChannelGrid(channel); afterCalls != beforeCalls+1 {
		t.Fatalf("idle final flush calls = %d, want %d", afterCalls, beforeCalls+1)
	}
	if info := tab.Info(); info.GridRevision != beforeRevision {
		t.Fatalf("unchanged final flush emitted a new grid revision: %+v", info)
	}
}

func TestGridRuntimeAutomaticHandoffFencesDetachedIntent(t *testing.T) {
	events := make(chan ControlEvent, 32)
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	handoffStarted := make(chan struct{})
	unblock := make(chan struct{})
	var handoffOnce sync.Once
	var unblockOnce sync.Once
	unblockAttach := func() { unblockOnce.Do(func() { close(unblock) }) }
	defer unblockAttach()
	manager := NewManager(Config{
		Connector: connector,
		Terminals: terminals,
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			if event.Topic == TopicTerminalControl {
				payload := event.Payload.(ControlEvent)
				if payload.Controller == "client-b" {
					handoffOnce.Do(func() { close(handoffStarted) })
					<-unblock
				}
				events <- payload
			}
			return nil
		}),
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "grid-handoff", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	started := make(chan struct{})
	release := make(chan struct{})
	channel.resizeHook = func(_ context.Context, cols, _ uint32) error {
		if cols == 90 {
			close(started)
			<-release
		}
		return nil
	}
	first := resizeAsync(manager, tab.ID, "client-a", 90, 30)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first resize did not start")
	}
	second := resizeAsync(manager, tab.ID, "client-a", 100, 40)
	waitFor(t, func() bool {
		snapshot, err := manager.GridSnapshot(tab.ID)
		return err == nil && snapshot.Pending && snapshot.DesiredGrid == (terminalgrid.Grid{Cols: 100, Rows: 40})
	})
	if err := manager.DetachClient(tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	attached := make(chan error, 1)
	go func() {
		_, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1"})
		attached <- err
	}()
	close(release)
	select {
	case <-handoffStarted:
	case <-time.After(time.Second):
		t.Fatal("automatic handoff did not reach its control event")
	}
	latest := resizeAsync(manager, tab.ID, "client-b", 120, 50)
	waitFor(t, func() bool {
		snapshot, err := manager.GridSnapshot(tab.ID)
		return err == nil && snapshot.DesiredGrid == (terminalgrid.Grid{Cols: 120, Rows: 50})
	})
	unblockAttach()
	select {
	case err := <-attached:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic handoff did not complete")
	}
	if err := <-latest; err != nil {
		t.Fatal(err)
	}
	for _, result := range []<-chan error{first, second} {
		if err := <-result; err != nil && !errors.Is(err, ErrStaleGeneration) && !errors.Is(err, ErrNotController) {
			t.Fatalf("detached resize error = %v", err)
		}
	}
	calls, cols, rows := fakeChannelGrid(channel)
	if cols != 120 || rows != 50 {
		t.Fatalf("handoff transport grid = %dx%d", cols, rows)
	}
	if cols, rows := fakeTerminalGrid(terminals.terminal(tab.ID)); cols != 120 || rows != 50 {
		t.Fatalf("handoff terminal grid = %dx%d", cols, rows)
	}
	committed := committedGridEvents(events)
	if len(committed) < 1 || len(committed) > 2 || calls != len(committed)+1 {
		t.Fatalf("handoff calls/events = %d/%+v", calls, committed)
	}
	for _, event := range committed {
		if event.Cols == 90 && event.Rows == 30 {
			t.Fatalf("detached grid committed: %+v", committed)
		}
	}
	last := committed[len(committed)-1]
	if last.Cols != 120 || last.Rows != 50 {
		t.Fatalf("last handoff grid event = %+v", last)
	}
	if info := tab.Info(); info.Controller != "client-b" || info.GridRevision != uint64(len(committed)) {
		t.Fatalf("handoff revision state = %+v, events %+v", info, committed)
	}
}

func TestGridRuntimeHiddenOwnershipHandoffs(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		name := "release-claim"
		if automatic {
			name = "detach-attach"
		}
		t.Run(name, func(t *testing.T) {
			events := make(chan ControlEvent, 16)
			manager, connector, terminals, session := openGridTestSession(t, events)
			tab := openTestTab(t, manager, session, "client-a", "a-1")
			channel := connector.transport(0).channel(0)
			if err := manager.SetVisible(tab.ID, false); err != nil {
				t.Fatal(err)
			}
			if automatic {
				if err := manager.DetachClient(tab.ID, "client-a"); err != nil {
					t.Fatal(err)
				}
				if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1"}); err != nil {
					t.Fatal(err)
				}
			} else {
				if released, err := manager.Release(tab.ID, "client-a"); err != nil || !released {
					t.Fatalf("release = %v, err = %v", released, err)
				}
				if _, err := manager.Claim(tab.ID, "client-b"); err != nil {
					t.Fatal(err)
				}
			}
			if err := manager.Resize(context.Background(), tab.ID, "client-b", 91, 27); !errors.Is(err, ErrHidden) {
				t.Fatalf("hidden handoff resize error = %v", err)
			}
			if calls, _, _ := fakeChannelGrid(channel); calls != 0 {
				t.Fatalf("hidden handoff reached transport %d times", calls)
			}
			if !terminals.terminal(tab.ID).isHidden() {
				t.Fatal("ownership handoff changed core visibility")
			}
			if err := manager.SetVisible(tab.ID, true); err != nil {
				t.Fatal(err)
			}
			if err := manager.FlushResize(context.Background(), tab.ID, "client-b"); err != nil {
				t.Fatal(err)
			}
			if cols, rows := fakeTerminalGrid(terminals.terminal(tab.ID)); cols != 91 || rows != 27 {
				t.Fatalf("visible handoff terminal grid = %dx%d", cols, rows)
			}
		})
	}
}

func TestGridRuntimeConcurrentVisibilityTransitionsStayConsistent(t *testing.T) {
	events := make(chan ControlEvent, 16)
	manager, _, terminals, session := openGridTestSession(t, events)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	terminal := terminals.terminal(tab.ID)
	hideStarted := make(chan struct{})
	releaseHide := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHide) }) }
	defer release()
	terminal.visibilityHook = func(visible bool) {
		if !visible {
			close(hideStarted)
			<-releaseHide
		}
	}
	hideDone := make(chan error, 1)
	go func() { hideDone <- manager.SetVisible(tab.ID, false) }()
	select {
	case <-hideStarted:
	case <-time.After(time.Second):
		t.Fatal("hide did not reach the core terminal")
	}
	showDone := make(chan error, 1)
	go func() { showDone <- manager.SetVisible(tab.ID, true) }()
	select {
	case err := <-showDone:
		t.Fatalf("show overtook a blocked hide: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	if err := <-hideDone; err != nil {
		t.Fatal(err)
	}
	if err := <-showDone; err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.GridSnapshot(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Mode != terminalgrid.ModeController || terminal.isHidden() {
		t.Fatalf("visibility state = mode %v, core hidden %v", snapshot.Mode, terminal.isHidden())
	}
}

func TestGridRuntimeCloseBoundsNonCooperativeResize(t *testing.T) {
	events := make(chan ControlEvent, 16)
	manager, connector, terminals, session := openGridTestSession(t, events)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	starts := make(chan uint32, 4)
	release := make(chan struct{})
	finished := make(chan struct{})
	var releaseOnce sync.Once
	releaseTransport := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseTransport()
	channel.resizeHook = func(_ context.Context, cols, _ uint32) error {
		starts <- cols
		<-release
		if cols == 100 {
			close(finished)
		}
		return nil
	}
	first := resizeAsync(manager, tab.ID, "client-a", 100, 40)
	select {
	case cols := <-starts:
		if cols != 100 {
			t.Fatalf("first transport grid cols = %d", cols)
		}
	case <-time.After(time.Second):
		t.Fatal("resize did not start")
	}
	second := resizeAsync(manager, tab.ID, "client-a", 110, 45)
	waitFor(t, func() bool {
		snapshot, err := manager.GridSnapshot(tab.ID)
		return err == nil && snapshot.Pending && snapshot.DesiredGrid == (terminalgrid.Grid{Cols: 110, Rows: 45})
	})
	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("manager close waited indefinitely for non-cooperative resize")
	}
	if got := terminals.terminal(tab.ID).closes(); got != 1 {
		t.Fatalf("terminal close count = %d", got)
	}
	releaseTransport()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("non-cooperative transport was not released")
	}
	select {
	case cols := <-starts:
		t.Fatalf("close started a pending transport resize at %d cols", cols)
	case <-time.After(50 * time.Millisecond):
	}
	for _, result := range []<-chan error{first, second} {
		select {
		case <-result:
		case <-time.After(time.Second):
			t.Fatal("resize waiter survived manager close")
		}
	}
}
