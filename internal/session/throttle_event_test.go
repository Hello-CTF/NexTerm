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
	if entry.Recovered || entry.TabID != tab.ID || entry.InflightBytes <= 0 || entry.Version != 1 {
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
	if !recovery.Recovered || recovery.TabID != tab.ID || recovery.Version != 2 {
		t.Fatalf("recovery event = %+v", recovery)
	}
	if recovery.InflightBytes >= entry.InflightBytes {
		t.Fatalf("recovery bytes = %d, entry bytes = %d", recovery.InflightBytes, entry.InflightBytes)
	}

	stop, wg = flood()
	reentry := waitThrottle()
	if reentry.Recovered || reentry.Version != 3 {
		t.Fatalf("re-entry event = %+v", reentry)
	}
	close(stop)
	wg.Wait()

	if err := manager.DetachChannel("a-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-2", ReplayBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	stop, wg = flood()
	reattached := waitThrottle()
	if reattached.Recovered || reattached.TabID != tab.ID || reattached.Version != 4 {
		t.Fatalf("re-attached entry event = %+v", reattached)
	}
	close(stop)
	wg.Wait()
}
