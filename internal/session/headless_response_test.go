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

// A spectator does not answer: a tab watched only by a read-only preview
// leaves the answering to the headless screen.
func TestHeadlessScreenAnswersForSpectatorOnly(t *testing.T) {
	manager, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	ctx := context.Background()
	if err := manager.DetachChannel("cmd-channel"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "guest", ChannelID: "guest-view"}); err != nil {
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
	// The attached client answers this one itself.
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

// Guest input never makes the guest answer: a guest that kept control
// while the owner reattached leaves the answering to the owner's view, so
// the headless screen stays silent.
func TestOwnerViewAnswersWhileGuestControls(t *testing.T) {
	manager, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	ctx := context.Background()
	if err := manager.DetachChannel("cmd-channel"); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("ls\n"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "owner-view"}); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.GuestControl != "preview-share1" {
		t.Fatalf("guest control = %q after the owner reattached", info.GuestControl)
	}
	if err := channel.emit([]byte("\x1b[5n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 4 })
	// A detached-time query still gets its reply, which is written after
	// any reply to the attached-time one.
	if err := manager.DetachChannel("owner-view"); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("\x1b[c")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(channel.written()) > len("ls\n") })
	if written := string(channel.written()); written[:len("ls\n")] != "ls\n" || bytes.Contains([]byte(written), []byte("\x1b[0n")) {
		t.Fatalf("shell input = %q, want no reply to the query the owner view answered", written)
	}
}

// The answering client leaves while another view that never typed stays
// attached: that view takes over answering and the PTY size, so the headless
// screen stays silent; it answers again once no client is attached.
func TestRemainingClientAnswersAfterAnsweringClientLeaves(t *testing.T) {
	manager, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	ctx := context.Background()
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "observer-b"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DetachChannel("cmd-channel"); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != "client-b" {
		t.Fatalf("size owner = %q after the size owner left, want the remaining client", owner)
	}
	if err := channel.emit([]byte("\x1b[5n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 4 })
	if err := manager.DetachChannel("observer-b"); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("\x1b[6n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(channel.written()) > 0 })
	if written := channel.written(); bytes.Contains(written, []byte("\x1b[0n")) {
		t.Fatalf("shell input = %q, want no headless reply while client-b answered", written)
	}
}
