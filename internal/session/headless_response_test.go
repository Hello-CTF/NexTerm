package session

import (
	"bytes"
	"context"
	"testing"
)

// While a client is attached its own terminal emulator answers queries, so
// the headless screen stays silent; once nobody is attached the headless
// screen answers so programs never wait forever.
func TestHeadlessScreenAnswersOnlyWithoutAttachedClients(t *testing.T) {
	manager, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "")

	// Attached: a DA1 query gets no headless reply.
	if err := channel.emit([]byte("\x1b[c")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 3 })

	if err := manager.DetachChannel("cmd-channel"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(tab.ID); err != nil {
		t.Fatalf("tab gone after detach: %v", err)
	}
	// Detached: a DSR query is answered. Replies are written in order, so
	// a reply to the attached-time query would precede it.
	if err := channel.emit([]byte("\x1b[5n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(channel.written()) > 0 })
	if written := channel.written(); !bytes.Equal(written, []byte("\x1b[0n")) {
		t.Fatalf("shell input = %q, want only the detached-time DSR reply", written)
	}
}

// A paused subscriber refuses output and would answer late, so the headless
// screen answers while it is paused.
func TestHeadlessScreenAnswersForPausedSubscriber(t *testing.T) {
	manager, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	ctx := context.Background()
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "paused-b"}); err != nil {
		t.Fatal(err)
	}
	// client-a leaves: client-b stays attached without taking control.
	if err := manager.DetachChannel("cmd-channel"); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("\x1b[c")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 3 })
	// The client paused its channel (terminal_pause).
	tab.mu.Lock()
	paused := tab.subscribers["paused-b"]
	paused.paused = true
	tab.subscribers["paused-b"] = paused
	tab.mu.Unlock()
	if err := channel.emit([]byte("\x1b[5n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(channel.written()) > 0 })
	if written := channel.written(); !bytes.Equal(written, []byte("\x1b[0n")) {
		t.Fatalf("shell input = %q, want only the reply given while paused", written)
	}
}
