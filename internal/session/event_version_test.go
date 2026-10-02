package session

import (
	"context"
	"testing"
	"time"
)

type blockingControlEmitter struct {
	blockVersion uint64
	started      chan struct{}
	release      chan struct{}
	events       chan ControlEvent
}

func (e *blockingControlEmitter) EmitSessionEvent(_ context.Context, event Event) error {
	if event.Topic != TopicTerminalControl {
		return nil
	}
	payload := event.Payload.(ControlEvent)
	if payload.Version == e.blockVersion {
		close(e.started)
		<-e.release
	}
	e.events <- payload
	return nil
}

func TestBlockingEmitterCannotMakeControllerSnapshotAuthoritativelyStale(t *testing.T) {
	emitter := &blockingControlEmitter{
		blockVersion: 2,
		started:      make(chan struct{}),
		release:      make(chan struct{}),
		events:       make(chan ControlEvent, 4),
	}
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	<-emitter.events

	attached := make(chan error, 1)
	go func() {
		_, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-2"})
		attached <- err
	}()
	<-emitter.started
	if _, err := manager.Claim(tab.ID, "client-b"); err != nil {
		t.Fatal(err)
	}
	close(emitter.release)
	if err := <-attached; err != nil {
		t.Fatal(err)
	}
	newer := <-emitter.events
	older := <-emitter.events
	if newer.Version != 3 || newer.Controller != "client-b" {
		t.Fatalf("new event = %+v, want versioned client-b", newer)
	}
	if older.Version != 2 || older.Controller != "client-a" {
		t.Fatalf("delayed event = %+v, want older client-a", older)
	}
	latest := ControlEvent{}
	for _, event := range []ControlEvent{newer, older} {
		if event.Version > latest.Version {
			latest = event
		}
	}
	if latest.Controller != "client-b" || tab.Info().Controller != "client-b" {
		t.Fatalf("version-filtered controller = %q backend = %q", latest.Controller, tab.Info().Controller)
	}
}

func TestSessionStatusVersionsIncreaseAcrossReconnectAndDisconnect(t *testing.T) {
	var events []StatusEvent
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			if event.Topic == TopicSessionStatus {
				events = append(events, event.Payload.(StatusEvent))
			}
			return nil
		}),
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconnect(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disconnect(session.ID); err != nil {
		t.Fatal(err)
	}
	wantStatuses := []Status{StatusConnecting, StatusConnected, StatusReconnecting, StatusConnected, StatusDisconnected}
	if len(events) != len(wantStatuses) {
		t.Fatalf("status events = %+v", events)
	}
	for index, want := range wantStatuses {
		event := events[index]
		if event.Status != want || event.Version != uint64(index+1) {
			t.Errorf("event %d = %+v, want status %s version %d", index, event, want, index+1)
		}
	}
}
