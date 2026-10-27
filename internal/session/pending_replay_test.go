package session

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

func TestDefaultReplayFitsPendingQueueBeforeReceiverBinds(t *testing.T) {
	manager, _, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	tab.terminal.Feed(make([]byte, manager.defaultReplay))
	attached := make(chan error, 1)
	go func() {
		_, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: -1})
		attached <- err
	}()
	select {
	case err := <-attached:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("default replay blocked before a receiver could bind")
	}
	stats := manager.Hub().Stats()
	wantBytes := manager.defaultReplay + len(replayClear)
	if stats.QueuedBytes != wantBytes {
		t.Fatalf("pending replay bytes = %d, want %d", stats.QueuedBytes, wantBytes)
	}
	receiver := bindTestReceiver(t, manager, "b-1")
	assertFrameData(t, receiver, replayClear)
}

// TestAttachResumesFromConsumedOffset drives a real ring-backed terminal and
// proves an attach carrying the client's consumed anchor replays only the gap:
// the anchor frame pins the requested offset, no clear-screen control bytes are
// sent, and live output follows the resumed replay.
func TestAttachResumesFromConsumedOffset(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: terminalFactory{}})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "resume-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "resume-open")
	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("hello world")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 11 })

	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{
		ClientID: "client-b", ChannelID: "resume-1", ReplayBytes: -1,
		FromSeq: 5, FromEpoch: tab.streamEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, "resume-1")
	anchor := receiveTestAnchor(t, receiver)
	if anchor.Epoch != tab.streamEpoch || anchor.StreamSeq != 5 || anchor.ControlBytes != 0 || anchor.Expired {
		t.Fatalf("anchor = %+v, want epoch %q streamSeq 5 control 0 expired false", anchor, tab.streamEpoch)
	}
	// client-a answered the resumed output, so client-b must not answer it.
	assertSpans(t, "resume", anchor.Classes, span(5, 11, classAnswered))
	assertFrameData(t, receiver, []byte(" world"))
	if err := channel.emit([]byte("!")); err != nil {
		t.Fatal(err)
	}
	// Live output client-a answers is announced as answered to client-b.
	spans, out := receiveClassified(t, receiver, 1)
	assertSpans(t, "live", spans, span(11, 12, classAnswered))
	if string(out) != "!" {
		t.Fatalf("live output = %q, want !", out)
	}
}

// TestAttachExpiredAnchorFallsBackToFullReplay pins the explicit degradation:
// an anchor from a different stream epoch is rejected in the open - the anchor
// frame reports expired and the full clear-screen plus dump replay follows.
func TestAttachExpiredAnchorFallsBackToFullReplay(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: terminalFactory{}})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "expired-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "expired-open")
	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("hello world")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 11 })

	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{
		ClientID: "client-b", ChannelID: "expired-1", ReplayBytes: -1,
		FromSeq: 5, FromEpoch: "stale-epoch",
	}); err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, "expired-1")
	anchor := receiveTestAnchor(t, receiver)
	if anchor.Epoch != tab.streamEpoch || !anchor.Expired || anchor.ControlBytes != len(replayClear) || anchor.StreamSeq != 0 {
		t.Fatalf("anchor = %+v, want expired full replay from seq 0 with %d control bytes", anchor, len(replayClear))
	}
	assertFrameData(t, receiver, replayClear)
	assertFrameData(t, receiver, []byte("hello world"))
}

// TestAttachWithoutAnchorKeepsFullReplay proves clients that present no anchor
// keep the legacy behavior: clear screen, full replay, and a usable anchor for
// subsequent resumes.
func TestAttachWithoutAnchorKeepsFullReplay(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: terminalFactory{}})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "legacy-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "legacy-open")
	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("hello world")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 11 })

	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "legacy-1", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, "legacy-1")
	anchor := receiveTestAnchor(t, receiver)
	if anchor.Epoch != tab.streamEpoch || anchor.Expired || anchor.ControlBytes != len(replayClear) || anchor.StreamSeq != 0 {
		t.Fatalf("anchor = %+v, want fresh full replay anchor from seq 0", anchor)
	}
	assertFrameData(t, receiver, replayClear)
	assertFrameData(t, receiver, []byte("hello world"))
}

// TestAttachAnchorClampedByRingOverflow forces the anchor below the ring base
// and expects the expired fallback rather than a silently raised start.
func TestAttachAnchorClampedByRingOverflow(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: terminalFactory{}})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "clamp-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "clamp-open")
	tab.terminal.Feed(make([]byte, terminal.ScrollbackBytes+16))

	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{
		ClientID: "client-b", ChannelID: "clamp-1", ReplayBytes: 1024,
		FromSeq: 1, FromEpoch: tab.streamEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, "clamp-1")
	anchor := receiveTestAnchor(t, receiver)
	if !anchor.Expired || anchor.ControlBytes != len(replayClear) {
		t.Fatalf("anchor = %+v, want expired with %d control bytes", anchor, len(replayClear))
	}
	wantStart := uint64(terminal.ScrollbackBytes + 16 - 1024)
	if anchor.StreamSeq != wantStart {
		t.Fatalf("anchor.StreamSeq = %d, want %d", anchor.StreamSeq, wantStart)
	}
	assertFrameData(t, receiver, replayClear)
	frame := receiveTestFrame(t, receiver)
	if len(frame.Data) != 1024 {
		t.Fatalf("replay frame bytes = %d, want 1024", len(frame.Data))
	}
}

func receiveTestAnchor(t *testing.T, receiver *hub.Receiver) streamAnchor {
	t.Helper()
	frame := receiveTestFrame(t, receiver)
	if frame.Kind != hub.FrameJSON {
		t.Fatalf("first frame kind = %d, want JSON anchor", frame.Kind)
	}
	var anchor streamAnchor
	if err := json.Unmarshal(frame.Data, &anchor); err != nil {
		t.Fatalf("decode anchor frame: %v", err)
	}
	if anchor.Type != streamAnchorType {
		t.Fatalf("anchor type = %q, want %q", anchor.Type, streamAnchorType)
	}
	return anchor
}

// planSeqTerminal is a minimal sequenced TerminalState stub for planAttachReplay
// branch coverage without feeding a full scrollback ring.
type planSeqTerminal struct {
	latest  uint64
	base    uint64
	resumed []byte
	dump    []byte
}

func (s *planSeqTerminal) Feed([]byte)                     {}
func (s *planSeqTerminal) Resize(int, int) error           { return nil }
func (s *planSeqTerminal) SetResponseHandler(func([]byte)) {}
func (s *planSeqTerminal) Close()                          {}

func (s *planSeqTerminal) Dump(int) []byte { return s.dump }

func (s *planSeqTerminal) ReplayFrom(seq uint64, _ int) ([]byte, uint64, bool) {
	start := seq
	if start < s.base {
		return s.resumed, s.base, true
	}
	if start > s.latest {
		start = s.latest
	}
	if start >= s.latest {
		return nil, start, false
	}
	return s.resumed, start, false
}

func (s *planSeqTerminal) LatestSeq() uint64 { return s.latest }

// TestPlanAttachReplayBranches covers the resume vs full-replay decision
// matrix: adoptable anchor, ring-clamped anchor, epoch mismatch, oversized
// gap, uncapped replay, anchor ahead of the stream head, and terminals
// without sequence tracking.
func TestPlanAttachReplayBranches(t *testing.T) {
	manager := new(Manager)
	newTab := func(core *planSeqTerminal) *Tab {
		return &Tab{terminal: core, streamEpoch: "epoch-1"}
	}
	core := &planSeqTerminal{latest: 10, base: 2, resumed: []byte("rest"), dump: []byte("dump")}

	anchor, control, replay := manager.planAttachReplay(newTab(core), AttachOptions{ReplayBytes: 64, FromSeq: 5, FromEpoch: "epoch-1"}, true)
	if anchor == nil || anchor.Expired || anchor.StreamSeq != 5 || control != nil || string(replay) != "rest" {
		t.Fatalf("resume = %+v control %q replay %q", anchor, control, replay)
	}

	anchor, control, replay = manager.planAttachReplay(newTab(core), AttachOptions{ReplayBytes: 64, FromSeq: 1, FromEpoch: "epoch-1"}, true)
	if anchor == nil || !anchor.Expired || anchor.StreamSeq != 6 || !bytes.Equal(control, replayClear) || string(replay) != "dump" {
		t.Fatalf("clamped = %+v control %q replay %q", anchor, control, replay)
	}

	anchor, _, _ = manager.planAttachReplay(newTab(core), AttachOptions{ReplayBytes: 64, FromSeq: 5, FromEpoch: "other"}, true)
	if anchor == nil || !anchor.Expired {
		t.Fatalf("epoch mismatch = %+v, want expired", anchor)
	}

	anchor, _, replay = manager.planAttachReplay(newTab(core), AttachOptions{ReplayBytes: 2, FromSeq: 5, FromEpoch: "epoch-1"}, true)
	if anchor == nil || !anchor.Expired || string(replay) != "dump" {
		t.Fatalf("oversized gap = %+v replay %q, want expired full replay", anchor, replay)
	}

	anchor, control, replay = manager.planAttachReplay(newTab(core), AttachOptions{ReplayBytes: 0, FromSeq: 5, FromEpoch: "epoch-1"}, true)
	if anchor == nil || anchor.Expired || control != nil || string(replay) != "rest" {
		t.Fatalf("uncapped resume = %+v control %q replay %q", anchor, control, replay)
	}

	anchor, _, replay = manager.planAttachReplay(newTab(core), AttachOptions{ReplayBytes: 64, FromSeq: 99, FromEpoch: "epoch-1"}, true)
	if anchor == nil || anchor.Expired || anchor.StreamSeq != 10 || len(replay) != 0 {
		t.Fatalf("ahead anchor = %+v replay %q, want empty resume at head", anchor, replay)
	}

	anchor, control, replay = manager.planAttachReplay(newTab(core), AttachOptions{ReplayBytes: 64}, true)
	if anchor == nil || anchor.Expired || anchor.StreamSeq != 6 || !bytes.Equal(control, replayClear) || string(replay) != "dump" {
		t.Fatalf("no anchor = %+v control %q replay %q", anchor, control, replay)
	}

	plain := &Tab{terminal: &fakeTerminal{}, streamEpoch: "epoch-1"}
	anchor, control, replay = manager.planAttachReplay(plain, AttachOptions{ReplayBytes: 64, FromSeq: 5, FromEpoch: "epoch-1"}, true)
	if anchor != nil || !bytes.Equal(control, replayClear) || replay != nil {
		t.Fatalf("unsequenced terminal anchor = %+v control %q replay %q, want nil anchor", anchor, control, replay)
	}

	_, control, _ = manager.planAttachReplay(newTab(core), AttachOptions{ReplayBytes: 64}, false)
	if control != nil {
		t.Fatalf("open-time attach control = %q, want none", control)
	}
}
