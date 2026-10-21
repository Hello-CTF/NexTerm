package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

type blockingExitEmitter struct {
	started  chan struct{}
	release  chan struct{}
	exits    chan ExitEvent
	controls chan ControlEvent
}

func (e *blockingExitEmitter) EmitSessionEvent(_ context.Context, event Event) error {
	switch event.Topic {
	case TopicTerminalExit:
		payload := event.Payload.(ExitEvent)
		close(e.started)
		<-e.release
		e.exits <- payload
	case TopicTerminalControl:
		e.controls <- event.Payload.(ControlEvent)
	}
	return nil
}

func TestReconnectVersionSuppressesBlockedOldExitAndControl(t *testing.T) {
	emitter := &blockingExitEmitter{
		started: make(chan struct{}), release: make(chan struct{}),
		exits: make(chan ExitEvent, 1), controls: make(chan ControlEvent, 2),
	}
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Emitter: emitter,
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	connector := manager.connector.(*fakeConnector)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	<-emitter.controls
	if err := connector.transport(0).channel(0).Close(); err != nil {
		t.Fatal(err)
	}
	<-emitter.started
	if err := manager.Reconnect(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	close(emitter.release)
	oldExit := <-emitter.exits
	oldControl := <-emitter.controls
	reconnected := <-emitter.controls
	if reconnected.Version != 4 || reconnected.Exited {
		t.Fatalf("reconnect control = %+v, want version 4 exited=false", reconnected)
	}
	if oldExit.Version != 2 || oldControl.Version != 3 || !oldControl.Exited {
		t.Fatalf("old exit/control = %+v / %+v", oldExit, oldControl)
	}
	latestVersion := reconnected.Version
	for _, version := range []uint64{oldExit.Version, oldControl.Version} {
		if version > latestVersion {
			t.Fatalf("old generation version %d exceeded reconnect version %d", version, latestVersion)
		}
	}
	if tab.Info().Exited {
		t.Fatal("reconnect restored exited=true after old events")
	}
	if len(emitter.exits) != 0 || len(emitter.controls) != 0 {
		t.Fatal("unexpected duplicate exit/control event")
	}
}

func TestReconnectAbortDiscardsPendingControlEvents(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindWinRM)
	events := make(chan ControlEvent, 16)
	manager.emitter = EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic == TopicTerminalControl {
			events <- event.Payload.(ControlEvent)
		}
		return nil
	})
	good := openTestTab(t, manager, session, "client-good", "good-1")
	bad := openTestTab(t, manager, session, "client-bad", "bad-1")
	drainControlEvents(events)
	bad.grid.Close()
	session.mu.Lock()
	session.status = StatusReconnecting
	session.reconnecting = true
	session.reconnectDone = make(chan struct{})
	generation := session.generation + 1
	session.generation = generation
	session.mu.Unlock()
	transport := newTransportHandle(connector.transport(0))
	for attempt := 0; attempt < 16; attempt++ {
		good.mu.Lock()
		before := good.eventVersion
		good.mu.Unlock()
		opened := map[*Tab]*replacementChannel{
			good: {channel: newChannelHandle(newFakeChannel(0))},
			bad:  {channel: newChannelHandle(newFakeChannel(0))},
		}
		_, err := manager.commitReconnect(session, transport, opened, nil, generation)
		if err == nil {
			t.Fatal("failed reattach was accepted")
		}
		good.mu.Lock()
		changed := good.eventVersion > before
		good.mu.Unlock()
		if changed {
			break
		}
		if attempt == 15 {
			t.Fatal("did not reach successful-then-failed reattach ordering")
		}
	}
	good.mu.Lock()
	next := good.controlEventLocked()
	good.mu.Unlock()
	manager.emit(context.Background(), TopicTerminalControl, next)
	select {
	case event := <-events:
		if event.Version != next.Version {
			t.Fatalf("emitted version = %d, want next control version %d", event.Version, next.Version)
		}
	case <-time.After(time.Second):
		t.Fatal("next control event was not emitted after abort")
	}
	select {
	case event := <-events:
		t.Fatalf("aborted control event leaked: %+v", event)
	default:
	}
}

// A reaping manager shutdown must not let a tab's pump emit a terminal exit
// event: the reap's transport close wakes the replacement channel's pump
// before the teardown marks the tab destroying, and only the session-closed
// suppression keeps that teardown wake from reaching subscribers.
func TestManagerCloseSuppressesReapedTabExitEvent(t *testing.T) {
	var mu sync.Mutex
	var exits []ExitEvent
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			if event.Topic == TopicTerminalExit {
				mu.Lock()
				exits = append(exits, event.Payload.(ExitEvent))
				mu.Unlock()
			}
			return nil
		}),
		ReconnectBackoff: []time.Duration{0},
	})
	ctx := context.Background()
	session, err := manager.Connect(ctx, Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	connector := manager.connector.(*fakeConnector)
	openTestTab(t, manager, session, "client-a", "a-1")
	if err := connector.transport(0).channel(0).Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(exits) == 1
	})
	if err := manager.Reconnect(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(exits) != 1 {
		t.Fatalf("exit events = %d, want only the pre-reconnect exit; the reaped tab's teardown wake escaped as a terminal event", len(exits))
	}
}
