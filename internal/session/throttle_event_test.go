package session

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

func TestBackpressureEmitsTerminalThrottled(t *testing.T) {
	throttled := make(chan ThrottleEvent, 4)
	emitter := EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic != TopicTerminalThrottled {
			return nil
		}
		if payload, ok := event.Payload.(ThrottleEvent); ok {
			select {
			case throttled <- payload:
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

	select {
	case event := <-throttled:
		if event.TabID != tab.ID {
			t.Fatalf("throttle event tab = %q, want %q", event.TabID, tab.ID)
		}
		if event.InflightBytes <= 0 {
			t.Fatalf("throttle inflight bytes = %d", event.InflightBytes)
		}
	case <-time.After(5 * time.Second):
		close(stop)
		wg.Wait()
		t.Fatal("missing terminal://throttled event under backpressure")
	}
	close(stop)
	wg.Wait()
}
