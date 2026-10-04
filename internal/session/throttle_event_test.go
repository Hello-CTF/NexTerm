package session

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

func TestThrottleEventPairsEntryWithRealDrain(t *testing.T) {
	events := make(chan ThrottleEvent, 16)
	emitter := EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic != TopicTerminalThrottled {
			return nil
		}
		if payload, ok := event.Payload.(ThrottleEvent); ok {
			select {
			case events <- payload:
			default:
			}
		}
		return nil
	})
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "throttle", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)

	flood := func() (chan struct{}, *sync.WaitGroup) {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			frame := bytes.Repeat([]byte("x"), 1024)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := channel.emit(frame); err != nil {
					return
				}
			}
		}()
		return stop, &wg
	}
	waitThrottle := func() ThrottleEvent {
		t.Helper()
		select {
		case event := <-events:
			return event
		case <-time.After(5 * time.Second):
			t.Fatal("missing terminal://throttled event")
			return ThrottleEvent{}
		}
	}

	stop, wg := flood()
	entry := waitThrottle()
	if entry.Recovered || entry.TabID != tab.ID || entry.ChannelID != "a-1" || entry.InflightBytes <= 0 || entry.Version != 1 {
		t.Fatalf("entry event = %+v", entry)
	}

	select {
	case event := <-events:
		t.Fatalf("recovered while backlog still present: %+v", event)
	case <-time.After(300 * time.Millisecond):
	}

	close(stop)
	wg.Wait()

	receiver := bindTestReceiver(t, manager, "a-1")
	var recovery ThrottleEvent
	drained := 0
	for {
		select {
		case event := <-events:
			recovery = event
		default:
		}
		if recovery.Version != 0 {
			break
		}
		drained++
		if drained > 4096 {
			t.Fatal("queue drained without a recovery event")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		frame, err := receiver.Next(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.Ack(frame.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	if !recovery.Recovered || recovery.TabID != tab.ID || recovery.ChannelID != "a-1" || recovery.Version != 2 {
		t.Fatalf("recovery event = %+v", recovery)
	}
	if recovery.InflightBytes >= entry.InflightBytes {
		t.Fatalf("recovery bytes = %d, entry bytes = %d", recovery.InflightBytes, entry.InflightBytes)
	}

	stop, wg = flood()
	reentry := waitThrottle()
	if reentry.Recovered || reentry.ChannelID != "a-1" || reentry.Version != 3 {
		t.Fatalf("re-entry event = %+v", reentry)
	}
	close(stop)
	wg.Wait()

	if err := manager.DetachChannel("a-1"); err != nil {
		t.Fatal(err)
	}
	detachRecovery := waitThrottle()
	if !detachRecovery.Recovered || detachRecovery.ChannelID != "a-1" || detachRecovery.Version != 4 {
		t.Fatalf("detach recovery event = %+v", detachRecovery)
	}
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-2", ReplayBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	stop, wg = flood()
	reattached := waitThrottle()
	if reattached.Recovered || reattached.TabID != tab.ID || reattached.ChannelID != "a-2" || reattached.Version != 5 {
		t.Fatalf("re-attached entry event = %+v", reattached)
	}
	close(stop)
	wg.Wait()
}

func throttleCollector() (Emitter, chan ThrottleEvent) {
	events := make(chan ThrottleEvent, 16)
	emitter := EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic != TopicTerminalThrottled {
			return nil
		}
		if payload, ok := event.Payload.(ThrottleEvent); ok {
			select {
			case events <- payload:
			default:
			}
		}
		return nil
	})
	return emitter, events
}

func waitThrottleEvent(t *testing.T, events chan ThrottleEvent) ThrottleEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("missing terminal://throttled event")
		return ThrottleEvent{}
	}
}

func assertNoThrottleEvent(t *testing.T, events chan ThrottleEvent, wait time.Duration) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected terminal://throttled event: %+v", event)
	case <-time.After(wait):
	}
}

func TestThrottleRecoveryAggregatesAcrossChannels(t *testing.T) {
	emitter, events := throttleCollector()
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "throttle-ab", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: 1024}); err != nil {
		t.Fatal(err)
	}

	fill := func(channelID string) (cancel context.CancelFunc) {
		t.Helper()
		frame := bytes.Repeat([]byte("x"), 1024)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go func() {
			for {
				if err := manager.bus.SendBinary(ctx, channelID, frame); err != nil {
					return
				}
			}
		}()
		return cancel
	}
	drainChannel := func(channelID string) {
		t.Helper()
		receiver := bindTestReceiver(t, manager, channelID)
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			frame, err := receiver.Next(ctx)
			cancel()
			if err != nil {
				return
			}
			if err := receiver.Ack(frame.Sequence); err != nil {
				t.Fatal(err)
			}
		}
	}

	cancelA := fill("a-1")
	entryA := waitThrottleEvent(t, events)
	cancelA()
	if entryA.Recovered || entryA.ChannelID != "a-1" || entryA.Version != 1 {
		t.Fatalf("entry A = %+v", entryA)
	}
	cancelB := fill("b-1")
	entryB := waitThrottleEvent(t, events)
	cancelB()
	if entryB.Recovered || entryB.ChannelID != "b-1" || entryB.Version != 2 {
		t.Fatalf("entry B = %+v", entryB)
	}

	drainChannel("a-1")
	assertNoThrottleEvent(t, events, 300*time.Millisecond)

	drainChannel("b-1")
	recovery := waitThrottleEvent(t, events)
	if !recovery.Recovered || recovery.TabID != tab.ID || recovery.ChannelID != "b-1" || recovery.Version != 3 {
		t.Fatalf("recovery = %+v", recovery)
	}
}

func TestThrottleDiscardPendingPairsRecovery(t *testing.T) {
	emitter, events := throttleCollector()
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "throttle-discard", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")

	frame := bytes.Repeat([]byte("x"), 1024)
	fillCtx, stopFill := context.WithCancel(context.Background())
	t.Cleanup(stopFill)
	sends := make(chan error, 600)
	go func() {
		for i := 0; i < 600; i++ {
			sends <- manager.bus.SendBinary(fillCtx, "a-1", frame)
		}
	}()
	entry := waitThrottleEvent(t, events)
	if entry.Recovered || entry.TabID != tab.ID || entry.ChannelID != "a-1" || entry.Version != 1 {
		t.Fatalf("entry = %+v", entry)
	}
	for len(sends) > 0 {
		<-sends
	}

	if err := manager.bus.DiscardPending("a-1"); err != nil {
		t.Fatal(err)
	}
	recovery := waitThrottleEvent(t, events)
	if !recovery.Recovered || recovery.ChannelID != "a-1" || recovery.InflightBytes != 0 || recovery.Version != 2 {
		t.Fatalf("discard recovery = %+v", recovery)
	}
	select {
	case err := <-sends:
		if err != nil {
			t.Fatalf("blocked producer not released: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked producer still blocked after discard")
	}
}
