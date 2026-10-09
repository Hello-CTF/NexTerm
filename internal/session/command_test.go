package session

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/terminal/shellintegr"
)

// TestTabTracksCommandLifecycle drives OSC 133 reports through the tab feed
// path and asserts the passive CommandTracker snapshot advances through a
// full command lifecycle without disturbing the stream.
func TestTabTracksCommandLifecycle(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector,
		Terminals: newFakeTerminalFactory(),
	})
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "cmd-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "cmd-channel")
	channel := connector.transport(0).channel(0)

	if state := tab.CommandState(); state.Running || state.Sequence != 0 || state.HasLastExitCode {
		t.Fatalf("fresh CommandState() = %+v; want idle zero", state)
	}

	if err := channel.emit([]byte("\x1b]133;B\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.CommandState().Running })
	if state := tab.CommandState(); !state.Running || state.Sequence != 1 {
		t.Fatalf("after B CommandState() = %+v; want running, sequence 1", state)
	}

	// Split the finish sequence across two emits to exercise chunk joins.
	if err := channel.emit([]byte("\x1b]133;D;")); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("3\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !tab.CommandState().Running })
	state := tab.CommandState()
	if state.Running || state.Sequence != 1 || !state.HasLastExitCode || state.LastExitCode != 3 {
		t.Fatalf("after D;3 CommandState() = %+v; want idle, sequence 1, exit 3", state)
	}
}

// TestTabCommandStateIgnores133FreeStream asserts output with no OSC 133
// leaves the command snapshot at its idle zero value, the fallback for
// shells without command integration.
func TestTabCommandStateIgnores133FreeStream(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector,
		Terminals: newFakeTerminalFactory(),
	})
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "plain-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "plain-channel")
	channel := connector.transport(0).channel(0)

	if err := channel.emit([]byte("user@host:~$ ls\r\nfile1\r\n\x1b]7;file://h/home/u\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.Info().Cwd == "/home/u" })
	if state := tab.CommandState(); state != (shellintegr.CommandState{}) {
		t.Fatalf("CommandState() = %+v; want idle zero for a 133-free stream", state)
	}
}
