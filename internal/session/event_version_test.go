package session

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
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

func TestBlockingEmitterCannotMakeControlSnapshotAuthoritativelyStale(t *testing.T) {
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
	if err := manager.DetachChannel("a-1"); err != nil {
		t.Fatal(err)
	}
	close(emitter.release)
	if err := <-attached; err != nil {
		t.Fatal(err)
	}
	older := <-emitter.events
	newer := <-emitter.events
	if newer.Version != 3 || newer.Subscribers != 1 {
		t.Fatalf("new event = %+v, want versioned single subscriber", newer)
	}
	if older.Version != 2 || older.Subscribers != 2 {
		t.Fatalf("delayed event = %+v, want older two subscribers", older)
	}
	latest := ControlEvent{}
	for _, event := range []ControlEvent{newer, older} {
		if event.Version > latest.Version {
			latest = event
		}
	}
	if latest.Subscribers != 1 || tab.Info().Subscribers != 1 {
		t.Fatalf("version-filtered subscribers = %d backend = %d", latest.Subscribers, tab.Info().Subscribers)
	}
}

func TestControlEventVersionAndEnqueueAreSerialized(t *testing.T) {
	events := make(chan ControlEvent, 2)
	manager := NewManager(Config{Emitter: EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic == TopicTerminalControl {
			events <- event.Payload.(ControlEvent)
		}
		return nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	tab := &Tab{ID: "ordered-control", session: &Session{}, subscribers: make(map[string]subscriber), terminal: &fakeTerminal{}}
	first := tab.controlEventLocked()
	secondDone := make(chan ControlEvent, 1)
	go func() {
		secondDone <- tab.controlEventLocked()
	}()
	second := <-secondDone
	manager.emit(context.Background(), TopicTerminalControl, second)
	manager.emit(context.Background(), TopicTerminalControl, first)
	firstEmitted := <-events
	secondEmitted := <-events
	if firstEmitted.Version != 1 || secondEmitted.Version != 2 {
		t.Fatalf("control events = %+v, %+v, want versions 1,2", firstEmitted, secondEmitted)
	}
}

func TestClosedControlOwnerCleanupUsesTabIncarnation(t *testing.T) {
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	manager := NewManager(Config{Emitter: EmitterFunc(func(ctx context.Context, event Event) error {
		if ipc.EventOwnerID(ctx) == "old-owner" {
			close(oldStarted)
			<-releaseOld
		}
		return nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	oldTab := &Tab{ID: "owner-aba", session: &Session{userID: "old-owner"}, subscribers: make(map[string]subscriber), terminal: &fakeTerminal{}, closed: true, exited: true}
	oldEvent := oldTab.controlEventLocked()
	oldQueue := recordingControlOrderForTab(oldTab)
	newTab := &Tab{ID: oldTab.ID, session: &Session{userID: "new-owner"}, subscribers: make(map[string]subscriber), terminal: &fakeTerminal{}}
	newOpen := newTab.controlEventLocked()
	newQueue := recordingControlOrderForTab(newTab)
	manager.emit(context.Background(), TopicTerminalControl, newOpen)
	if got := manager.tabOwners[oldTab.ID]; got != "new-owner" {
		t.Fatalf("owner after reopen = %q, want new-owner", got)
	}
	oldDone := make(chan struct{})
	go func() {
		manager.emit(context.Background(), TopicTerminalControl, oldEvent)
		close(oldDone)
	}()
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old close did not reach the emitter")
	}
	if got := manager.tabOwners[oldTab.ID]; got != "new-owner" {
		t.Fatalf("old close removed the new owner before new close: %q", got)
	}
	newTab.closed = true
	newTab.exited = true
	newClose := newTab.controlEventLocked()
	manager.emit(context.Background(), TopicTerminalControl, newClose)
	if _, ok := manager.controlOwners[newQueue]; ok {
		t.Fatal("new owner was not cleaned after new close")
	}
	if got := manager.controlOwners[oldQueue]; got != "old-owner" {
		t.Fatalf("old owner before final drain = %q, want old-owner", got)
	}
	close(releaseOld)
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("old close did not finish")
	}
	if _, ok := manager.controlOwners[oldQueue]; ok {
		t.Fatal("old owner was not cleaned after old close drained")
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
