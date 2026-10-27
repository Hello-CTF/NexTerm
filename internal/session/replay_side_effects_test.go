package session

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/hub"
)

// receiveClassified reads frames until at least want output bytes arrived,
// collecting the stream-class frames announced on the way.
func receiveClassified(t *testing.T, receiver *hub.Receiver, want int) ([]classSpan, []byte) {
	t.Helper()
	var spans []classSpan
	var out []byte
	for len(out) < want {
		frame := receiveTestFrame(t, receiver)
		if frame.Kind == hub.FrameJSON {
			var class streamClassFrame
			if err := json.Unmarshal(frame.Data, &class); err != nil || class.Type != streamClassType {
				t.Fatalf("unexpected JSON frame %s", frame.Data)
			}
			spans = append(spans, class.classSpan)
			continue
		}
		out = append(out, frame.Data...)
	}
	return spans, out
}

func span(from, to int, kind string) classSpan {
	return classSpan{From: uint64(from), To: uint64(to), Kind: kind}
}

func assertSpans(t *testing.T, what string, got []classSpan, want ...classSpan) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s classes = %+v, want %+v", what, got, want)
	}
}

// recoveredTab turns a real-terminal tab into a recovered durable tab whose
// tail ends at helper offset tailEnd (stream sequence and helper offset
// coincide: the ring starts empty).
func recoveredTab(t *testing.T, tab *Tab, tailEnd int) uint64 {
	t.Helper()
	tab.mu.Lock()
	defer tab.mu.Unlock()
	tab.durable = replayAttachment{}
	tab.durableFed = 0
	tab.displayFrom = 0
	tab.replayUntil = int64(tailEnd)
	return tab.generation
}

// A full clear-screen replay is one replay span; an adopted resume over live
// output carries no classes.
func TestAttachAnchorClassifiesReplay(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: terminalFactory{}})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "replay-classes", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "classes-open")
	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("hello world")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 11 })

	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "classes-full", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	full := bindTestReceiver(t, manager, "classes-full")
	anchor := receiveTestAnchor(t, full)
	assertSpans(t, "full attach", anchor.Classes, span(0, 11, classReplay))

	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{
		ClientID: "client-c", ChannelID: "classes-resume", ReplayBytes: -1, FromSeq: 5, FromEpoch: tab.streamEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	resumed := bindTestReceiver(t, manager, "classes-resume")
	assertSpans(t, "resume", receiveTestAnchor(t, resumed).Classes)
	assertFrameData(t, resumed, []byte(" world"))
}

// A recovered durable tail renders without events: no headless replies, no
// command records, no unread mark. A client attached mid-tail gets the rest
// announced as replay ahead of the bytes; a client that resumes after the
// tail finished (kicked as a slow consumer mid-tail) still gets the rest of
// the tail as replay.
func TestRecoveredTailStaysReplayAcrossAttachAndResume(t *testing.T) {
	sink := &fakeCommandSink{}
	manager, tab, channel, _ := openCommandTestTab(t, sink, "")
	ctx := context.Background()
	if err := manager.DetachChannel("cmd-channel"); err != nil {
		t.Fatal(err)
	}
	head := []byte("user@host:~$ old\r\n\x1b]133;B\x1b\\\x1b[c\x1b[6n")
	rest := []byte("old output\x1b]52;c;b2xk\x07\x1b]9;old\x07\r\n\x1b]133;D;0\x1b\\")
	tailEnd := len(head) + len(rest)
	live := []byte("user@host:~$ new\r\n\x1b]133;B\x1b\\new output\r\n\x1b]133;D;0\x1b\\")
	generation := recoveredTab(t, tab, tailEnd)

	if err := manager.feedDurable(ctx, tab, generation, head, int64(len(head))); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "tail-mid", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	mid := bindTestReceiver(t, manager, "tail-mid")
	assertSpans(t, "mid-tail attach", receiveTestAnchor(t, mid).Classes, span(0, len(head), classReplay))
	receiveClassified(t, mid, len(replayClear)+len(head))
	if err := manager.feedDurable(ctx, tab, generation, rest, int64(tailEnd)); err != nil {
		t.Fatal(err)
	}
	if got := tab.Info().LastOutputAt; got != 0 {
		t.Fatalf("replayed tail set lastOutputAt %d: the tab would be marked unread", got)
	}
	if err := manager.feedDurable(ctx, tab, generation, live, int64(tailEnd+len(live))); err != nil {
		t.Fatal(err)
	}
	spans, data := receiveClassified(t, mid, len(rest)+len(live))
	assertSpans(t, "live fanout", spans, span(len(head), tailEnd, classReplay))
	if !bytes.Equal(data, append(append([]byte(nil), rest...), live...)) {
		t.Fatalf("fanout = %q", data)
	}

	// Case 1: the client was kicked after the head and resumes now.
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{
		ClientID: "client-b", ChannelID: "tail-resume", ReplayBytes: -1, FromSeq: uint64(len(head)), FromEpoch: tab.streamEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	resumed := bindTestReceiver(t, manager, "tail-resume")
	assertSpans(t, "resume after tail", receiveTestAnchor(t, resumed).Classes, span(len(head), tailEnd, classReplay))

	// No replayed query was answered into the shell: with nobody attached,
	// a live DSR is the first and only reply (replies are written in order).
	for _, channelID := range []string{"tail-mid", "tail-resume"} {
		if err := manager.DetachChannel(channelID); err != nil {
			t.Fatal(err)
		}
	}
	end := tailEnd + len(live)
	if err := manager.feedDurable(ctx, tab, generation, []byte("\x1b[5n"), int64(end+4)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(channel.written()) > 0 })
	if written := channel.written(); !bytes.Equal(written, []byte("\x1b[0n")) {
		t.Fatalf("shell input = %q, want only the live DSR reply", written)
	}
	waitFor(t, func() bool { return len(sink.snapshot()) == 1 })
	if records := sink.snapshot(); len(records) != 1 || !strings.Contains(records[0].Command, "new") {
		t.Fatalf("command records = %+v, want only the live command", records)
	}
	if tab.Info().LastOutputAt == 0 {
		t.Fatal("live output did not update lastOutputAt")
	}
}

// Case 2: output fed while no answering client was attached was answered
// headlessly; a resume over a mixed gap marks exactly that suffix, so the
// shell gets one reply per query.
func TestResumeMarksHeadlesslyAnsweredOutput(t *testing.T) {
	manager, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	ctx := context.Background()
	if err := channel.emit([]byte("x")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 1 })
	// Attached: the client answers this one.
	if err := channel.emit([]byte("\x1b[6n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 5 })
	if err := manager.DetachChannel("cmd-channel"); err != nil {
		t.Fatal(err)
	}
	// Detached: answered headlessly.
	if err := channel.emit([]byte("\x1b[5n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(channel.written()) > 0 })
	if written := channel.written(); !bytes.Equal(written, []byte("\x1b[0n")) {
		t.Fatalf("shell input = %q, want only the detached-time reply", written)
	}

	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{
		ClientID: "client-a", ChannelID: "mixed-resume", ReplayBytes: -1, FromSeq: 1, FromEpoch: tab.streamEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, "mixed-resume")
	assertSpans(t, "mixed resume", receiveTestAnchor(t, receiver).Classes, span(5, 9, classAnswered))
}

// Case 3: a recovery anchor inside a tail keeps the refused tail bytes
// replay.
func TestRecoveryAnchorKeepsTailReplay(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: terminalFactory{}, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "recovery-tail", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.CreateTab(ctx, CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(ctx, info.ID, AttachOptions{ClientID: "client-a", ChannelID: "rec-1"}); err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, "rec-1")
	receiveTestAnchor(t, receiver)
	receiveClassified(t, receiver, len(replayClear))
	tail := bytes.Repeat([]byte("t"), 30)
	generation := recoveredTab(t, tab, len(tail))
	if err := manager.feedDurable(ctx, tab, generation, tail, int64(len(tail))); err != nil {
		t.Fatal(err)
	}
	if err := manager.feedDurable(ctx, tab, generation, []byte("live"), int64(len(tail)+4)); err != nil {
		t.Fatal(err)
	}
	receiveClassified(t, receiver, len(tail)+4)
	// The client accepted only 10 bytes of the tail.
	if _, _, err := manager.RecoverChannelFor(ctx, info.ID, "rec-1", "client-a", attachmentIdFor(t, manager, info.ID, "rec-1"), 10); err != nil {
		t.Fatal(err)
	}
	frame := receiveTestFrame(t, receiver)
	var anchor streamAnchor
	if err := json.Unmarshal(frame.Data, &anchor); err != nil || anchor.Type != streamAnchorType {
		t.Fatalf("recovery frame = %s", frame.Data)
	}
	assertSpans(t, "recovery", anchor.Classes, span(10, len(tail), classReplay))
}

// Case 4: a notice inside the tail and a tail cut short by output the helper
// dropped keep a mid-tail client in sync: exactly the replayed bytes are
// announced, the live rest is not.
func TestRecoveredTailGapKeepsClassesExact(t *testing.T) {
	manager, tab, _, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	ctx := context.Background()
	if err := manager.DetachChannel("cmd-channel"); err != nil {
		t.Fatal(err)
	}
	generation := recoveredTab(t, tab, 100)
	if err := manager.feedDurable(ctx, tab, generation, bytes.Repeat([]byte("a"), 40), 40); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "gap-mid", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, "gap-mid")
	receiveTestAnchor(t, receiver)
	receiveClassified(t, receiver, len(replayClear)+40)
	// Offsets 40..80 were dropped by the helper; the chunk crosses the end.
	if err := manager.feedDurable(ctx, tab, generation, bytes.Repeat([]byte("b"), 40), 120); err != nil {
		t.Fatal(err)
	}
	banner := len(durableHistoryGapBanner)
	spans, data := receiveClassified(t, receiver, banner+40)
	assertSpans(t, "gap fanout", spans, span(40, 40+banner, classReplay), span(40+banner, 60+banner, classReplay))
	if !bytes.HasSuffix(data, bytes.Repeat([]byte("b"), 40)) {
		t.Fatalf("fanout = %q", data)
	}
	tab.mu.Lock()
	until := tab.replayUntil
	tab.mu.Unlock()
	if until != 0 {
		t.Fatalf("replay window still open until %d", until)
	}
}

// Case 5: preview spectators do not answer queries, so they never stop the
// headless screen from answering.
func TestSpectatorsDoNotSuppressHeadlessReplies(t *testing.T) {
	manager, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	ctx := context.Background()
	if err := manager.DetachChannel("cmd-channel"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "viewer", ChannelID: "spectator"}); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("\x1b[5n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Equal(channel.written(), []byte("\x1b[0n")) })
}
