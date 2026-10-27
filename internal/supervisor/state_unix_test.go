//go:build unix

package supervisor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
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
	t.Fatalf("timed out waiting for %s; last state %+v (prompt %+v)", what, last, promptOf(last))
	return nil
}

func promptOf(state *SessionState) *SessionPrompt {
	if state == nil {
		return nil
	}
	return state.Prompt
}

func promptReady(state *SessionState) bool {
	return state.State == base.SessionNeedsMe && state.Prompt != nil
}

func interactiveShellSession(t *testing.T, supervisor *Supervisor) *Session {
	t.Helper()
	session, err := supervisor.Create(context.Background(), CreateOptions{
		ID:      ids.New(),
		Command: []string{"/bin/sh", "-i"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C", "PS1=$ ", "ENV=/dev/null", "HOME=" + t.TempDir()},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestStateTapGatingOnRealPTY(t *testing.T) {
	supervisor := testSupervisor(t)

	t.Run("line mode program", func(t *testing.T) {
		session := testScriptSession(t, supervisor, `printf 'Proceed? [y/N] '; read answer; echo "got=$answer"; sleep 30`)
		state := waitSessionState(t, session, "yes/no prompt", promptReady)
		prompt := state.Prompt
		if !prompt.Tappable || prompt.Submit != base.TapSubmitEnter || prompt.Options[0].Send != "y\r" {
			t.Fatalf("prompt = %+v", prompt)
		}
		if _, err := session.Write([]byte(prompt.Options[0].Send)); err != nil {
			t.Fatal(err)
		}
		after := waitSessionState(t, session, "answer consumed", func(s *SessionState) bool { return s.PromptEpoch > prompt.Epoch })
		if after.Prompt != nil && after.Prompt.Epoch == prompt.Epoch {
			t.Fatalf("prompt kept its epoch after input: %+v", after.Prompt)
		}
		readUntilState(t, session, "got=y")
	})

	t.Run("raw mode program", func(t *testing.T) {
		session := testScriptSession(t, supervisor, `stty -icanon -echo; printf 'Proceed? [y/N] '; dd bs=1 count=1 >/dev/null 2>&1; sleep 30`)
		prompt := waitSessionState(t, session, "yes/no prompt", promptReady).Prompt
		if !prompt.Tappable || prompt.Submit != base.TapSubmitSeparate || prompt.EnterKey != "\r" || prompt.Options[0].Send != "y" {
			t.Fatalf("prompt = %+v", prompt)
		}
	})

	t.Run("numbered menu needs line mode", func(t *testing.T) {
		session := testScriptSession(t, supervisor, `stty -icanon; printf '1) apple\r\n2) pear\r\n\r\nPick a fruit: '; sleep 30`)
		prompt := waitSessionState(t, session, "numbered menu", promptReady).Prompt
		if prompt.Tappable || prompt.Blocked != base.TapBlockedNotLineMode {
			t.Fatalf("prompt = %+v", prompt)
		}
	})

	t.Run("shell prompt per OSC 133", func(t *testing.T) {
		session := testScriptSession(t, supervisor, `printf '\033]133;A\007Proceed? [y/N] \033]133;B\007'; sleep 30`)
		state := waitSessionState(t, session, "inspected shell prompt", func(s *SessionState) bool { return s.Prompt != nil })
		if state.State != base.SessionIdle || state.Prompt.Tappable || state.Prompt.Blocked != base.TapBlockedShellPrompt {
			t.Fatalf("state %s prompt %+v", state.State, state.Prompt)
		}
	})

	t.Run("interactive shell in the foreground", func(t *testing.T) {
		session := interactiveShellSession(t, supervisor)
		// The shell's own read builtin: the shell holds the foreground.
		if _, err := session.Write([]byte("printf 'Proceed? [y/N] '; read answer\n")); err != nil {
			t.Fatal(err)
		}
		prompt := waitSessionState(t, session, "prompt read by the shell", promptReady).Prompt
		if prompt.Tappable || prompt.Blocked != base.TapBlockedShellForeground {
			t.Fatalf("prompt = %+v", prompt)
		}
	})

	t.Run("job started by an interactive shell", func(t *testing.T) {
		session := interactiveShellSession(t, supervisor)
		if _, err := session.Write([]byte("/bin/sh -c \"printf 'Proceed? [y/N] '; read answer\"\n")); err != nil {
			t.Fatal(err)
		}
		prompt := waitSessionState(t, session, "prompt read by a job", promptReady).Prompt
		if !prompt.Tappable || prompt.Submit != base.TapSubmitEnter {
			t.Fatalf("prompt = %+v", prompt)
		}
	})
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
	for name, lister := range map[string]base.DurableDescriptorLister{
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
	answered := waitStreamState(t, stream, "answered", func(s *SessionState) bool { return s.State != base.SessionNeedsMe })
	if answered.PromptEpoch <= pushed.PromptEpoch {
		t.Fatalf("input did not advance the epoch: %d -> %d", pushed.PromptEpoch, answered.PromptEpoch)
	}
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
	if prompt := session.CurrentPrompt(); prompt != nil {
		t.Fatalf("prompt reported with detection disabled: %+v", prompt)
	}
}
