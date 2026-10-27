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
