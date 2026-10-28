package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// attentionRecorder keeps every published attention event.
type attentionRecorder struct {
	mu     sync.Mutex
	events []AttentionEvent
	owners []string
	wake   chan struct{}
}

func newAttentionRecorder() *attentionRecorder {
	return &attentionRecorder{wake: make(chan struct{}, 1)}
}

func (r *attentionRecorder) EmitSessionEvent(ctx context.Context, event Event) error {
	if event.Topic != TopicTerminalAttention {
		return nil
	}
	r.mu.Lock()
	r.events = append(r.events, event.Payload.(AttentionEvent))
	r.owners = append(r.owners, ipc.EventOwnerID(ctx))
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

// screenTerminal adds a real screen model to the fake terminal, as the tab's
// own terminal model provides one: the observed tracker inspects it.
type screenTerminal struct {
	*fakeTerminal
	screenMu sync.Mutex
	screen   *terminal.Emulator
}

func (s *screenTerminal) Feed(data []byte) {
	s.fakeTerminal.Feed(data)
	s.screenMu.Lock()
	s.screen.Write(data)
	s.screenMu.Unlock()
}

func (s *screenTerminal) Snapshot() terminal.Snapshot {
	s.screenMu.Lock()
	defer s.screenMu.Unlock()
	return s.screen.Snapshot()
}

func screenTerminals() TerminalFactory {
	return TerminalFactoryFunc(func(config TerminalConfig) (TerminalState, error) {
		return &screenTerminal{
			fakeTerminal: &fakeTerminal{cols: int(config.Cols), rows: int(config.Rows)},
			screen:       terminal.NewEmulator(int(config.Cols), int(config.Rows)),
		}, nil
	})
}

func TestAttentionObservedForTabsOutsideAHelper(t *testing.T) {
	recorder := newAttentionRecorder()
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: screenTerminals(), Emitter: recorder, OpenAccess: true})
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
	if err := manager.Write(ctx, tab.ID, tab.Info().SizeOwner, []byte("y\r")); err != nil {
		t.Fatal(err)
	}
	recorder.wait(t, tab.ID, "answered", func(event AttentionEvent) bool {
		return event.State == base.SessionIdle && !event.Seen && event.Reason == "input"
	})
	// The state carries the client that typed last.
	if listed, err = manager.ListAttentionFor(ctx); err != nil || len(listed) != 1 || listed[0].LastInputClient != "client-a" {
		t.Fatalf("listed %+v (%v), want lastInputClient client-a", listed, err)
	}

	// A notification carries its text to every client.
	if err := channel.emit([]byte("\x1b]777;notify;Build;finished\a")); err != nil {
		t.Fatal(err)
	}
	notified := recorder.wait(t, tab.ID, "notification", func(event AttentionEvent) bool {
		return event.State == base.SessionNeedsMe && event.Reason == "notify"
	})
	if notified.Title != "Build" || notified.Body != "finished" {
		t.Fatalf("notification state %+v", notified)
	}
	if err := manager.Write(ctx, tab.ID, tab.Info().SizeOwner, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	cleared := recorder.wait(t, tab.ID, "answered notification", func(event AttentionEvent) bool { return event.State == base.SessionIdle })
	if cleared.Title != "" || cleared.Body != "" {
		t.Fatalf("idle state kept the notification text: %+v", cleared)
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
	asked := recorder.wait(t, tab.ID, "bell", attentionIs(base.SessionNeedsMe, base.ConfidenceSignal, AttentionSourceObserved))

	// The connection drops: the earlier state is not kept as if it were live,
	// and the ended runtime's tracker cannot publish over the final state.
	_ = connector.transport(0).Close()
	want := AttentionEvent{TabID: tab.ID, State: AttentionUnknown, Source: AttentionSourceObserved, Reason: "disconnected", Version: asked.Version + 1}
	gone := recorder.wait(t, tab.ID, "unknown", func(event AttentionEvent) bool { return event.Version > asked.Version })
	if !gone.sameAs(want) || gone.Version != want.Version {
		t.Fatalf("state after the connection dropped = %+v, want %+v", gone, want)
	}
	listed, err := manager.ListAttentionFor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !listed[0].sameAs(want) || listed[0].Version != want.Version {
		t.Fatalf("listed %+v, want %+v", listed, want)
	}
}

// One account never lists, marks or receives another account's attention
// states.
func TestAttentionIsOwnerScoped(t *testing.T) {
	recorder := newAttentionRecorder()
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: recorder})
	t.Cleanup(func() { _ = manager.Close() })
	alice := ipc.WithUserID(context.Background(), "user-a")
	bob := ipc.WithUserID(context.Background(), "user-b")
	session, err := manager.Connect(alice, Asset{ID: "docker-owned", Kind: KindDocker})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "alice", "ch-a")
	if err := connector.transport(0).channel(0).emit([]byte("approve\a")); err != nil {
		t.Fatal(err)
	}
	asked := recorder.wait(t, tab.ID, "bell", attentionIs(base.SessionNeedsMe, base.ConfidenceSignal, AttentionSourceObserved))

	recorder.mu.Lock()
	for index, owner := range recorder.owners {
		if owner != "user-a" {
			t.Errorf("event %+v carried owner %q, want user-a", recorder.events[index], owner)
		}
	}
	recorder.mu.Unlock()

	listed, err := manager.ListAttentionFor(bob)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("another account listed %+v", listed)
	}
	if err := manager.MarkAttentionSeenFor(bob, tab.ID, asked.Since); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another account marked seen: %v, want ErrForbidden", err)
	}
	owned, err := manager.ListAttentionFor(alice)
	if err != nil || len(owned) != 1 || owned[0].Seen || owned[0].Version != asked.Version {
		t.Fatalf("owner list after the refused mark = %+v, %v", owned, err)
	}
	if _, err := manager.ListAttentionFor(context.Background()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("anonymous list = %v, want ErrForbidden", err)
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
