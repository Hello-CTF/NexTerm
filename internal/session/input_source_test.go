package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type inputSourceRecorder struct {
	mu     sync.Mutex
	events []InputSourceEvent
}

func (r *inputSourceRecorder) emitter() Emitter {
	return EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic != TopicTerminalInputSource {
			return nil
		}
		payload, ok := event.Payload.(InputSourceEvent)
		if !ok {
			return nil
		}
		r.mu.Lock()
		r.events = append(r.events, payload)
		r.mu.Unlock()
		return nil
	})
}

func (r *inputSourceRecorder) take() []InputSourceEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	events := r.events
	r.events = nil
	return events
}

func openInputSourceTab(t *testing.T) (*Manager, *Tab, *fakeChannel, *inputSourceRecorder) {
	t.Helper()
	recorder := &inputSourceRecorder{}
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: recorder.emitter(),
		ReconnectMax: 2, ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset-input-source", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	return manager, tab, connector.transport(0).channel(0), recorder
}

// An internal write marked as AI input reports its tab and source once the
// bytes reached the terminal; the written bytes are unchanged.
func TestAIInternalWriteEmitsInputSource(t *testing.T) {
	manager, tab, channel, recorder := openInputSourceTab(t)
	ctx := WithInputSource(context.Background(), InputSourceAI)
	if err := manager.WriteInternal(ctx, tab.ID, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if got := string(channel.written()); got != "ls\r" {
		t.Fatalf("written = %q, want %q", got, "ls\r")
	}
	events := recorder.take()
	if len(events) != 1 || events[0] != (InputSourceEvent{TabID: tab.ID, Source: InputSourceAI}) {
		t.Fatalf("input source events = %+v", events)
	}
}

// User input and unmarked internal writes report no input source.
func TestUserAndUnmarkedWritesEmitNoInputSource(t *testing.T) {
	manager, tab, channel, recorder := openInputSourceTab(t)
	if err := manager.Write(context.Background(), tab.ID, "client-a", []byte("user")); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteInternal(context.Background(), tab.ID, []byte("internal")); err != nil {
		t.Fatal(err)
	}
	if got := string(channel.written()); got != "userinternal" {
		t.Fatalf("written = %q", got)
	}
	if events := recorder.take(); len(events) != 0 {
		t.Fatalf("unexpected input source events = %+v", events)
	}
}

// A refused write is not reported as AI input.
func TestFailedAIWriteEmitsNoInputSource(t *testing.T) {
	manager, tab, _, recorder := openInputSourceTab(t)
	ctx, cancel := context.WithCancel(WithInputSource(context.Background(), InputSourceAI))
	cancel()
	if err := manager.WriteInternal(ctx, tab.ID, []byte("ls")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write err = %v", err)
	}
	if events := recorder.take(); len(events) != 0 {
		t.Fatalf("unexpected input source events = %+v", events)
	}
}
