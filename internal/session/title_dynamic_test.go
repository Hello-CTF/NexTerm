package session

import (
	"context"
	"testing"
	"time"
)

// controlCollector captures emitted control events for assertions.
func controlCollector() (Emitter, chan ControlEvent) {
	events := make(chan ControlEvent, 64)
	return EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic == TopicTerminalControl {
			if payload, ok := event.Payload.(ControlEvent); ok {
				events <- payload
			}
		}
		return nil
	}), events
}

func drainControl(events chan ControlEvent) []ControlEvent {
	collected := []ControlEvent{}
	for {
		select {
		case event := <-events:
			collected = append(collected, event)
		default:
			return collected
		}
	}
}

func waitForControlTitle(t *testing.T, events chan ControlEvent, title string) ControlEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Title == title {
				return event
			}
		case <-deadline:
			t.Fatalf("no control event carried title %q", title)
		}
	}
}

// An OSC 0/2 dynamic title reported by the application becomes the tab title
// and reaches every attached client through a control event.
func TestOSCDynamicTitleUpdatesTabAndEmitsControlEvent(t *testing.T) {
	emitter, events := controlCollector()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{Connector: connector, Terminals: terminals, Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	terminals.terminal(tab.ID).setTitle("build log")
	if err := channel.emit([]byte("\x1b]0;build log\x07")); err != nil {
		t.Fatal(err)
	}
	waitForControlTitle(t, events, "build log")
	if got := tab.Info().Title; got != "build log" {
		t.Fatalf("tab title = %q, want the OSC title", got)
	}
}

// A later OSC title change replaces the previous dynamic title with a fresh
// control event.
func TestOSCDynamicTitleChangeEmitsAgain(t *testing.T) {
	emitter, events := controlCollector()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{Connector: connector, Terminals: terminals, Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	terminals.terminal(tab.ID).setTitle("one")
	if err := channel.emit([]byte("x")); err != nil {
		t.Fatal(err)
	}
	waitForControlTitle(t, events, "one")
	terminals.terminal(tab.ID).setTitle("two")
	if err := channel.emit([]byte("y")); err != nil {
		t.Fatal(err)
	}
	waitForControlTitle(t, events, "two")
	if got := tab.Info().Title; got != "two" {
		t.Fatalf("tab title = %q, want the latest OSC title", got)
	}
}

// The custom title from RenameTab takes precedence over the dynamic OSC
// title: once the user names the tab, later OSC reports never override it.
func TestCustomTitlePrecedesOSCDynamicTitle(t *testing.T) {
	emitter, events := controlCollector()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{Connector: connector, Terminals: terminals, Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if _, err := manager.RenameTab(context.Background(), tab.ID, "deploy"); err != nil {
		t.Fatal(err)
	}
	// Consume events until the rename itself is observed (the attach event
	// may still be queued ahead of it).
	waitForControlTitle(t, events, "deploy")
	terminals.terminal(tab.ID).setTitle("sneaky")
	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("x")); err != nil {
		t.Fatal(err)
	}
	// Give the feed a beat, then assert no further title event arrived.
	time.Sleep(100 * time.Millisecond)
	for _, event := range drainControl(events) {
		if event.Title != "deploy" {
			t.Fatalf("control event title = %q, want the custom title kept", event.Title)
		}
	}
	if got := tab.Info().Title; got != "deploy" {
		t.Fatalf("tab title = %q, want the custom title", got)
	}
}

// Without an OSC report the tab title stays empty and no title control event
// fires.
func TestNoOSCTitleKeepsTabTitleEmpty(t *testing.T) {
	emitter, _ := controlCollector()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{Connector: connector, Terminals: terminals, Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("plain output")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := tab.Info().Title; got != "" {
		t.Fatalf("tab title = %q, want empty", got)
	}
}
