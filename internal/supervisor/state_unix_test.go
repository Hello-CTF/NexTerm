//go:build unix

package supervisor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func waitSessionState(t *testing.T, session *Session, what string, match func(*SessionState) bool) *SessionState {
	t.Helper()
	deadline := time.Now().Add(testReadTimeout)
	var last *SessionState
	for time.Now().Before(deadline) {
		last = session.Info().State
		if last != nil && match(last) {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last state %+v", what, last)
	return nil
}

// readUntilState attaches and reads the session output until marker.
func readUntilState(t *testing.T, session *Session, marker string) {
	t.Helper()
	attachment, err := session.attach(AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Detach() }()
	readUntil(t, attachment, marker)
}

func TestStateVisibleWithoutClientsAndPushedToAttached(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	// The bell rings well after the input that releases it, so it is not
	// taken as the reaction to that input.
	session := testScriptSession(t, supervisor, `read go; sleep 1.5; printf 'needs you\007'; read again; sleep 30`)

	stream, err := client.Attach(context.Background(), session.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	initial := stream.SessionState()
	if initial == nil {
		t.Fatal("attach reply carried no state")
	}
	if _, err := stream.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	pushed := waitStreamState(t, stream, "pushed bell", func(s *SessionState) bool { return s.State == base.SessionNeedsMe })
	if pushed.Confidence != base.ConfidenceSignal || !hasEvent(pushed.Events, base.SessionEventBell) {
		t.Fatalf("pushed state %+v", pushed)
	}

	// A list from a client that never attached sees the same state.
	listed, err := NewClient(server.SocketPath(), supervisor.StateDir()).ListWithEvents(context.Background(), initial.LastEventSeq)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].State == nil || listed[0].State.State != base.SessionNeedsMe || !hasEvent(listed[0].State.Events, base.SessionEventBell) {
		t.Fatalf("listed %+v", listed)
	}
	newest := listed[0].State.LastEventSeq
	quiet, err := client.ListWithEvents(context.Background(), newest)
	if err != nil {
		t.Fatal(err)
	}
	if len(quiet[0].State.Events) != 0 || quiet[0].State.EventsDropped {
		t.Fatalf("events after the newest sequence: %+v", quiet[0].State)
	}
	plain, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plain[0].State == nil || plain[0].State.State != base.SessionNeedsMe || plain[0].State.Events != nil {
		t.Fatalf("plain list state %+v", plain[0].State)
	}

	// Durable descriptors carry the state end to end, local and remote,
	// without the events polling callers do not read.
	for name, lister := range map[string]base.DurableProvider{
		"local":  NewProvider(supervisor),
		"remote": NewRemoteProvider(client),
	} {
		descriptors, err := lister.ListDurableDescriptors(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(descriptors) != 1 || descriptors[0].State == nil || descriptors[0].State.State != base.SessionNeedsMe || descriptors[0].State.Events != nil {
			t.Fatalf("%s descriptors %+v", name, descriptors)
		}
	}

	// Input from the attached client answers the bell and is pushed back.
	if _, err := stream.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	waitStreamState(t, stream, "answered", func(s *SessionState) bool { return s.State != base.SessionNeedsMe })
}

func TestStateSeenReachesEveryAttachedClient(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	session := testScriptSession(t, supervisor, `read go; sleep 1.5; printf 'needs you\007'; sleep 30`)
	desktop, err := testClient(t, server).Attach(context.Background(), session.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = desktop.Close() }()
	phone, err := testClient(t, server).Attach(context.Background(), session.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = phone.Close() }()
	if _, err := desktop.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	asked := waitStreamState(t, phone, "bell", func(s *SessionState) bool { return s.State == base.SessionNeedsMe })
	waitStreamState(t, desktop, "bell on the desktop", func(s *SessionState) bool { return s.State == base.SessionNeedsMe })
	if asked.Seen {
		t.Fatalf("new attention already seen: %+v", asked)
	}

	if err := desktop.MarkStateSeen(context.Background(), asked.Since); err != nil {
		t.Fatal(err)
	}
	seen := waitStreamState(t, phone, "seen on the other client", func(s *SessionState) bool { return s.Seen })
	if seen.State != base.SessionNeedsMe || !seen.Since.Equal(asked.Since) {
		t.Fatalf("seen state %+v, want the bell state since %v", seen, asked.Since)
	}
	listed, err := testClient(t, server).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].State == nil || !listed[0].State.Seen {
		t.Fatalf("listed %+v", listed)
	}

	// An embedded attachment applies the same rule: a since that names no
	// current state changes nothing.
	attachment, err := session.attach(AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Detach() }()
	if err := attachment.MarkStateSeen(asked.Since.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if state := attachment.SessionState(0); !state.Seen || !state.Since.Equal(asked.Since) {
		t.Fatalf("a stale since changed the state: %+v", state)
	}
}

// A transient helper failure is not a seen_unsupported answer, so the client retries it.
func TestStateSeenTransientErrorIsNotUnsupported(t *testing.T) {
	for _, err := range []error{ErrUnavailable, ErrProtocol, ErrUnsupported, ErrClosed} {
		if got := translateError(err); errors.Is(got, durable.ErrSeenUnsupported) {
			t.Fatalf("translateError(%v) = %v, must not be durable.ErrSeenUnsupported", err, got)
		}
	}
	if got := translateError(ErrUnavailable); !errors.Is(got, durable.ErrUnavailable) {
		t.Fatalf("translateError(ErrUnavailable) = %v, want durable.ErrUnavailable", got)
	}
	// The wire code survives a round trip, so a helper with detection disabled is permanent too.
	if got := errorSentinel(errorCode(ErrStateSeenUnsupported)); !errors.Is(got, ErrStateSeenUnsupported) {
		t.Fatalf("wire round trip = %v, want ErrStateSeenUnsupported", got)
	}
	if got := translateError(ErrStateSeenUnsupported); !errors.Is(got, durable.ErrSeenUnsupported) {
		t.Fatalf("translateError(ErrStateSeenUnsupported) = %v, want durable.ErrSeenUnsupported", got)
	}
}

// A frame this helper does not know is refused without closing the
// connection.
func TestServerRefusesAnUnknownFrameAndKeepsServing(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	conn, err := testClient(t, server).dialConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, _, err := conn.request(context.Background(), frameType(250), struct{}{}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("unknown frame reply = %v; want a protocol error", err)
	}
	kind, _, err := conn.request(context.Background(), frameList, listOptions{})
	if err != nil || kind != frameListed {
		t.Fatalf("list after the unknown frame = %d, %v; want frameListed", kind, err)
	}
}

func waitStreamState(t *testing.T, stream *Stream, what string, match func(*SessionState) bool) *SessionState {
	t.Helper()
	timeout := time.After(testReadTimeout)
	for {
		if state := stream.SessionState(); state != nil && match(state) {
			return state
		}
		select {
		case <-stream.StateUpdates():
		case <-timeout:
			t.Fatalf("timed out waiting for %s; last %+v", what, stream.SessionState())
		}
	}
}

func hasEvent(events []SessionEvent, kind string) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func TestStateExitedAndRecovered(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	supervisor, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	session := testScriptSession(t, supervisor, `exit 3`)
	if err := session.Wait(context.Background()); err == nil {
		t.Fatal("expected exit error")
	}
	state := session.Info().State
	if state == nil || state.State != base.SessionExited || state.ExitCode == nil || *state.ExitCode != 3 {
		t.Fatalf("state = %+v", state)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restarted.Close() }()
	infos, err := restarted.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].State == nil || infos[0].State.State != base.SessionExited || infos[0].State.ExitCode == nil || *infos[0].State.ExitCode != 3 {
		t.Fatalf("recovered infos %+v", infos)
	}
}

func TestStateDetectionCanBeDisabled(t *testing.T) {
	supervisor, err := New(Config{StateDir: filepath.Join(t.TempDir(), "state"), CommandTimeout: 5 * time.Second, DisableStateDetection: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = supervisor.Close() }()
	session := testScriptSession(t, supervisor, `printf 'ring\007'; sleep 30`)
	if session.state != nil {
		t.Fatal("tracker created with detection disabled")
	}
	readUntilState(t, session, "ring")
	if state := session.Info().State; state != nil {
		t.Fatalf("state reported with detection disabled: %+v", state)
	}
}
