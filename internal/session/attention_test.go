package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// attentionRecorder keeps every published attention event.
type attentionRecorder struct {
	mu     sync.Mutex
	events []AttentionEvent
	wake   chan struct{}
}

func newAttentionRecorder() *attentionRecorder {
	return &attentionRecorder{wake: make(chan struct{}, 1)}
}

func (r *attentionRecorder) EmitSessionEvent(_ context.Context, event Event) error {
	if event.Topic != TopicTerminalAttention {
		return nil
	}
	r.mu.Lock()
	r.events = append(r.events, event.Payload.(AttentionEvent))
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// latest returns the tab's newest event by version, as a client keeps it.
func (r *attentionRecorder) latest(tabID string) (AttentionEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var newest AttentionEvent
	found := false
	for _, event := range r.events {
		if event.TabID == tabID && (!found || event.Version > newest.Version) {
			newest, found = event, true
		}
	}
	return newest, found
}

func (r *attentionRecorder) wait(t *testing.T, tabID, what string, match func(AttentionEvent) bool) AttentionEvent {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		if event, ok := r.latest(tabID); ok && match(event) {
			return event
		}
		select {
		case <-r.wake:
		case <-timeout:
			event, _ := r.latest(tabID)
			t.Fatalf("timed out waiting for %s; latest %+v", what, event)
		}
	}
}

func attentionIs(state, confidence, source string) func(AttentionEvent) bool {
	return func(event AttentionEvent) bool {
		return event.State == state && event.Confidence == confidence && event.Source == source
	}
}

func TestAttentionObservedForTabsOutsideAHelper(t *testing.T) {
	recorder := newAttentionRecorder()
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: recorder, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	session, err := manager.Connect(ctx, Asset{ID: "docker-1", Kind: KindDocker})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)

	recorder.wait(t, tab.ID, "initial idle", attentionIs(base.SessionIdle, base.ConfidenceSignal, AttentionSourceObserved))
	if err := channel.emit([]byte("building\r\n")); err != nil {
		t.Fatal(err)
	}
	recorder.wait(t, tab.ID, "running", attentionIs(base.SessionRunning, base.ConfidenceSignal, AttentionSourceObserved))
	if err := channel.emit([]byte("approve the change\a")); err != nil {
		t.Fatal(err)
	}
	asked := recorder.wait(t, tab.ID, "bell", attentionIs(base.SessionNeedsMe, base.ConfidenceSignal, AttentionSourceObserved))
	if asked.Reason != "bell" || asked.Seen || asked.Since == "" {
		t.Fatalf("bell state %+v", asked)
	}

	listed, err := manager.ListAttentionFor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].TabID != tab.ID || listed[0].State != base.SessionNeedsMe || listed[0].Version < asked.Version {
		t.Fatalf("listed %+v, want the bell state", listed)
	}

	// Seeing the state on one client marks it for every client.
	if err := manager.MarkAttentionSeenFor(ctx, tab.ID, asked.Since); err != nil {
		t.Fatal(err)
	}
	seen := recorder.wait(t, tab.ID, "seen", func(event AttentionEvent) bool { return event.Seen })
	if seen.State != base.SessionNeedsMe || seen.Since != asked.Since || seen.Version <= asked.Version {
		t.Fatalf("seen state %+v after %+v", seen, asked)
	}
	if err := manager.MarkAttentionSeenFor(ctx, tab.ID, "yesterday"); err == nil {
		t.Fatal("an unparsable since was accepted")
	}

	// Input answers it.
	if err := manager.Write(ctx, tab.ID, tab.Info().Controller, []byte("y\r")); err != nil {
		t.Fatal(err)
	}
	recorder.wait(t, tab.ID, "answered", func(event AttentionEvent) bool {
		return event.State == base.SessionIdle && !event.Seen && event.Reason == "input"
	})

	// A prompt shape after the output pauses is inferred.
	if err := channel.emit([]byte("Overwrite the file? [y/N] ")); err != nil {
		t.Fatal(err)
	}
	inferred := recorder.wait(t, tab.ID, "inferred prompt", attentionIs(base.SessionNeedsMe, base.ConfidenceHeuristic, AttentionSourceObserved))
	if inferred.Reason != "prompt" {
		t.Fatalf("inferred state %+v", inferred)
	}

	// The process exit ends the observed state.
	_ = channel.Close()
	recorder.wait(t, tab.ID, "exit", func(event AttentionEvent) bool { return event.State == base.SessionExited })
}

func TestAttentionUnknownWhileDisconnected(t *testing.T) {
	recorder := newAttentionRecorder()
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: recorder, OpenAccess: true,
		ReconnectMax: 1, ReconnectBackoff: []time.Duration{time.Hour},
	})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "docker-2", Kind: KindDocker})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if err := connector.transport(0).channel(0).emit([]byte("approve\a")); err != nil {
		t.Fatal(err)
	}
	recorder.wait(t, tab.ID, "bell", attentionIs(base.SessionNeedsMe, base.ConfidenceSignal, AttentionSourceObserved))

	// The connection drops: the earlier state is not kept as if it were live.
	_ = connector.transport(0).Close()
	gone := recorder.wait(t, tab.ID, "unknown", func(event AttentionEvent) bool { return event.State != base.SessionNeedsMe })
	if gone.State != AttentionUnknown && gone.State != base.SessionExited {
		t.Fatalf("state after the connection dropped = %+v", gone)
	}
	listed, err := manager.ListAttentionFor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range listed {
		if event.TabID == tab.ID && event.State == base.SessionNeedsMe {
			t.Fatalf("listed the stale state %+v", event)
		}
	}
}

func TestAttentionRequiresAKnownTab(t *testing.T) {
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	listed, err := manager.ListAttentionFor(context.Background())
	if err != nil || len(listed) != 0 {
		t.Fatalf("empty manager listed %+v, %v", listed, err)
	}
	err = manager.MarkAttentionSeenFor(context.Background(), "missing", time.Now().Format(time.RFC3339Nano))
	if err == nil {
		t.Fatal("seen was accepted for a missing tab")
	}
}
