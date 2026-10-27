package session

import (
	"bytes"
	"context"
	"testing"
	"time"
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

// Control alone answers nothing: a controller that only spectates (or has
// claimed without a subscriber) leaves the answering to the headless screen.
func TestHeadlessScreenAnswersForControllerWithoutAnsweringSubscriber(t *testing.T) {
	manager, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	ctx := context.Background()
	if err := manager.DetachChannel("cmd-channel"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "guest", ChannelID: "guest-view"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Claim(tab.ID, "guest"); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("\x1b[5n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Equal(channel.written(), []byte("\x1b[0n")) })
}

// A paused subscriber refuses output and would answer late, so the headless
// screen answers while it is paused.
func TestHeadlessScreenAnswersForPausedSubscriber(t *testing.T) {
	_, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	// The attached controller answers this one itself.
	if err := channel.emit([]byte("\x1b[c")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 3 })
	// The client paused its channel (terminal_pause).
	tab.mu.Lock()
	paused := tab.subscribers["cmd-channel"]
	paused.paused = true
	tab.subscribers["cmd-channel"] = paused
	tab.mu.Unlock()
	if err := channel.emit([]byte("\x1b[5n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(channel.written()) > 0 })
	if written := channel.written(); !bytes.Equal(written, []byte("\x1b[0n")) {
		t.Fatalf("shell input = %q, want only the reply given while paused", written)
	}
}

// A guest controlling through WriteGuest has no answering view, and the
// owner's still-attached view is an observer whose replies would be
// rejected: the headless screen answers, exactly once.
func TestHeadlessScreenAnswersWhileGuestControls(t *testing.T) {
	manager, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	ctx := context.Background()
	if _, err := manager.Release(tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("ls\n"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("\x1b[5n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(channel.written(), []byte("\x1b[0n")) })
	if written := channel.written(); bytes.Count(written, []byte("\x1b[0n")) != 1 {
		t.Fatalf("shell input = %q, want exactly one DSR reply", written)
	}
}

// The controller leaves while an observer view stays attached: orphan
// cleanup clears control, and the headless screen answers.
func TestHeadlessScreenAnswersAfterControllerLeaves(t *testing.T) {
	manager, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	ctx := context.Background()
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "observer-b"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DetachChannel("cmd-channel"); err != nil {
		t.Fatal(err)
	}
	if controller := tab.Info().Controller; controller != "" {
		t.Fatalf("controller = %q after the controller left", controller)
	}
	if err := channel.emit([]byte("\x1b[5n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Equal(channel.written(), []byte("\x1b[0n")) })
}
