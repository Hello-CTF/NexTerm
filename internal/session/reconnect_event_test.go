package session

import (
	"context"
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
	reconnected := <-emitter.controls
	if reconnected.Version != 4 || reconnected.Exited {
		t.Fatalf("reconnect control = %+v, want version 4 exited=false", reconnected)
	}
	close(emitter.release)
	oldExit := <-emitter.exits
	oldControl := <-emitter.controls
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
