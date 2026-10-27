package session

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/hub"
)

// receiveStreamBytes collects binary frames until at least want bytes arrived.
func receiveStreamBytes(t *testing.T, receiver *hub.Receiver, want int) []byte {
	t.Helper()
	var out []byte
	for len(out) < want {
		frame := receiveTestFrame(t, receiver)
		if frame.Kind != hub.FrameBinary {
			t.Fatalf("frame kind = %d after %d bytes, want binary", frame.Kind, len(out))
		}
		out = append(out, frame.Data...)
	}
	return out
}

// The anchor tells the client how many of the following bytes are replayed
// history: the clear-screen control plus the ring replay on a full attach,
// and exactly the resumed gap on an anchored reattach.
func TestAttachAnchorCountsReplayedBytes(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: terminalFactory{}})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "replay-count", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "count-open")
	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("hello world")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 11 })

	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "count-full", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	full := bindTestReceiver(t, manager, "count-full")
	anchor := receiveTestAnchor(t, full)
	if anchor.ReplayBytes != int64(anchor.ControlBytes)+11 {
		t.Fatalf("full attach replayBytes = %d, want control %d + 11", anchor.ReplayBytes, anchor.ControlBytes)
	}
	replayed := receiveStreamBytes(t, full, int(anchor.ReplayBytes))
	if !bytes.HasSuffix(replayed, []byte("hello world")) || len(replayed) != int(anchor.ReplayBytes) {
		t.Fatalf("replayed %q", replayed)
	}

	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{
		ClientID: "client-c", ChannelID: "count-resume", ReplayBytes: -1, FromSeq: 5, FromEpoch: tab.streamEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	resumed := bindTestReceiver(t, manager, "count-resume")
	if anchor := receiveTestAnchor(t, resumed); anchor.ReplayBytes != 0 {
		t.Fatalf("resume replayBytes = %d, want 0: the resumed gap is output this view missed", anchor.ReplayBytes)
	}
	assertFrameData(t, resumed, []byte(" world"))
	if err := channel.emit([]byte("!")); err != nil {
		t.Fatal(err)
	}
	assertFrameData(t, resumed, []byte("!"))
}

// A recovered durable tab's tail is fed through the pump like live output,
// but it is history: it must not answer queries into the live shell, record
// commands, or mark the tab as having new output, and a client attaching in
// the middle of it is told how many of the bytes it receives are replay.
func TestRecoveredDurableTailHasNoSideEffects(t *testing.T) {
	sink := &fakeCommandSink{}
	manager, tab, channel, _ := openCommandTestTab(t, sink, "")
	ctx := context.Background()

	head := []byte("user@host:~$ old\r\n\x1b]133;B\x1b\\\x1b[c\x1b[6n")
	rest := []byte("old output\r\n\x1b]133;D;0\x1b\\")
	tail := append(append([]byte(nil), head...), rest...)
	live := []byte("user@host:~$ new\r\n\x1b]133;B\x1b\\\x1b[cnew output\r\n\x1b]133;D;0\x1b\\")

	tab.mu.Lock()
	tab.durable = replayAttachment{}
	tab.durableFed = 0
	tab.displayFrom = 0
	tab.replayUntil = int64(len(tail))
	tab.replayPending = int64(len(tail))
	generation := tab.generation
	tab.mu.Unlock()

	if err := manager.feedDurable(ctx, tab, generation, head, int64(len(head))); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "tail-mid", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, "tail-mid")
	anchor := receiveTestAnchor(t, receiver)
	if err := manager.feedDurable(ctx, tab, generation, rest, int64(len(tail))); err != nil {
		t.Fatal(err)
	}
	if got := tab.Info().LastOutputAt; got != 0 {
		t.Fatalf("replayed tail set lastOutputAt %d: the tab would be marked unread", got)
	}
	if err := manager.feedDurable(ctx, tab, generation, live, int64(len(tail)+len(live))); err != nil {
		t.Fatal(err)
	}

	stream := receiveStreamBytes(t, receiver, int(anchor.ReplayBytes)+len(live))
	if !bytes.HasSuffix(stream[:anchor.ReplayBytes], tail) || !bytes.Equal(stream[anchor.ReplayBytes:], live) {
		t.Fatalf("replayBytes %d does not end at the tail: %q", anchor.ReplayBytes, stream)
	}

	// Only the live DA1 query is answered into the shell.
	reply := []byte("\x1b[?62;1;6;22c")
	waitFor(t, func() bool { return bytes.Contains(channel.written(), reply) })
	if written := channel.written(); bytes.Count(written, reply) != 1 || bytes.Contains(written, []byte("R")) {
		t.Fatalf("shell input = %q, want one live DA1 reply and no cursor report", written)
	}
	// Only the live command is recorded.
	waitFor(t, func() bool { return len(sink.snapshot()) == 1 })
	if records := sink.snapshot(); len(records) != 1 || !strings.Contains(records[0].Command, "new") {
		t.Fatalf("command records = %+v, want only the live command", records)
	}
	if tab.Info().LastOutputAt == 0 {
		t.Fatal("live output did not update lastOutputAt")
	}
}

// assertAttachAnnouncesRingReplay attaches a new client and checks that only
// the clear-screen control and the ring replay are announced as replay.
func assertAttachAnnouncesRingReplay(t *testing.T, manager *Manager, tab *Tab, channelID string) {
	t.Helper()
	want := int64(len(replayClear) + len(tab.terminal.Dump(manager.defaultReplay)))
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-z", ChannelID: channelID, ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, channelID)
	if anchor := receiveTestAnchor(t, receiver); anchor.ReplayBytes != want {
		t.Fatalf("replayBytes = %d, want control + ring replay = %d", anchor.ReplayBytes, want)
	}
}

// Output the helper dropped inside the recovered tail leaves fewer bytes to
// feed than announced: reaching the end of the tail closes the window, so a
// later attach never counts live output as replay.
func TestRecoveredTailGapClosesReplayWindow(t *testing.T) {
	manager, tab, _, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	ctx := context.Background()
	tab.mu.Lock()
	tab.durable = replayAttachment{}
	tab.replayUntil = 100
	tab.replayPending = 100
	generation := tab.generation
	tab.mu.Unlock()

	if err := manager.feedDurable(ctx, tab, generation, bytes.Repeat([]byte("a"), 40), 40); err != nil {
		t.Fatal(err)
	}
	// Offsets 40..80 were dropped by the helper; the chunk crosses the end.
	if err := manager.feedDurable(ctx, tab, generation, bytes.Repeat([]byte("b"), 40), 120); err != nil {
		t.Fatal(err)
	}
	tab.mu.Lock()
	pending, until := tab.replayPending, tab.replayUntil
	tab.mu.Unlock()
	if pending != 0 || until != 0 {
		t.Fatalf("replay window still open: pending %d until %d", pending, until)
	}
	assertAttachAnnouncesRingReplay(t, manager, tab, "gap-late")
}

// A prelude that could not be rendered is not counted for later attaches.
func TestStalePreludeIsNotAnnouncedAsReplay(t *testing.T) {
	manager, tab, _, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	prelude := []byte("\x1b[?1h")
	tab.mu.Lock()
	tab.durable = replayAttachment{}
	tab.historyPrelude = prelude
	tab.replayPending = int64(len(prelude))
	stale := tab.generation + 1
	tab.mu.Unlock()

	manager.startDurableReplay(context.Background(), tab, stale)
	tab.mu.Lock()
	pending := tab.replayPending
	tab.mu.Unlock()
	if pending != 0 {
		t.Fatalf("stale prelude left replayPending %d", pending)
	}
	assertAttachAnnouncesRingReplay(t, manager, tab, "prelude-late")
}
