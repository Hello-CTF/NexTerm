package session

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/hub"
)

// The PTY size follows the client that typed last, but only once the size
// owner has not typed for the handoff hold, so two clients typing in turn do
// not resize the PTY on every key. A client's size waits until it owns the
// size.
func TestSizeFollowsLastInputClientAfterHold(t *testing.T) {
	events := make(chan ControlEvent, 256)
	clock := newSessionTestClock()
	manager, _, _, session := openGridTestSessionAt(t, events, clock.Now)
	ctx := context.Background()
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1"}); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != "client-a" {
		t.Fatalf("size owner after attach = %q, want the first client", owner)
	}
	if err := manager.Resize(ctx, tab.ID, "client-b", 100, 30); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Cols != 80 || info.Rows != 24 {
		t.Fatalf("a follower's resize reached the PTY: %+v", info)
	}

	// Within the hold the owner keeps the size.
	if err := manager.Write(ctx, tab.ID, "client-b", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != "client-a" {
		t.Fatalf("size owner within the hold = %q", owner)
	}

	clock.Advance(sizeHandoffHold)
	if err := manager.Write(ctx, tab.ID, "client-b", []byte("2")); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != "client-b" {
		t.Fatalf("size owner after the hold = %q, want client-b", owner)
	}
	// The new owner's view sends its size, which now reaches the PTY.
	if err := manager.Resize(ctx, tab.ID, "client-b", 100, 30); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Cols != 100 || info.Rows != 30 {
		t.Fatalf("size owner's resize = %+v", info)
	}

	// client-a types right away: client-b keeps the size.
	if err := manager.Write(ctx, tab.ID, "client-a", []byte("3")); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != "client-b" {
		t.Fatalf("size moved back within the hold to %q", owner)
	}
	if last := tab.LastInputClient(); last != "client-a" {
		t.Fatalf("last input client = %q, want client-a", last)
	}

	// The size owner leaves: the most recently active remaining client
	// takes the size.
	if err := manager.DetachChannel("b-1"); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != "client-a" {
		t.Fatalf("size owner after it left = %q, want client-a", owner)
	}
	// With the last client gone nobody owns it.
	if err := manager.DetachChannel("a-1"); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != "" {
		t.Fatalf("size owner with no client attached = %q", owner)
	}
}

// The answering client is the most recent input client among the answering
// subscribers: its own emulator answers live output, every other client is
// told the range is answered, and the headless screen stays silent.
func TestAnsweringClientIsMostRecentInputClient(t *testing.T) {
	manager, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "")
	ctx := context.Background()
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-view"}); err != nil {
		t.Fatal(err)
	}
	viewA := bindTestReceiver(t, manager, "cmd-channel")
	viewB := bindTestReceiver(t, manager, "b-view")
	for _, view := range []*hub.Receiver{viewA, viewB} {
		if anchor := receiveTestAnchor(t, view); anchor.ControlBytes > 0 {
			assertFrameData(t, view, replayClear)
		}
	}

	// client-a attached first and answers.
	if err := channel.emit([]byte("\x1b[5n")); err != nil {
		t.Fatal(err)
	}
	spans, _ := receiveClassesSkippingOwner(t, viewB, 4)
	assertSpans(t, "client-b while client-a answers", spans, span(0, 4, classAnswered))

	if err := manager.Write(ctx, tab.ID, "client-b", []byte("k")); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("\x1b[6n")); err != nil {
		t.Fatal(err)
	}
	spans, _ = receiveClassesSkippingOwner(t, viewB, 4)
	assertSpans(t, "client-b after its input", spans)
	spans, out := receiveClassesSkippingOwner(t, viewA, 8)
	assertSpans(t, "client-a after client-b's input", spans, span(4, 8, classAnswered))
	if string(out) != "\x1b[5n\x1b[6n" {
		t.Fatalf("client-a output = %q", out)
	}

	tab.mu.Lock()
	forA, forB := tab.classes.within(0, 8, "client-a"), tab.classes.within(0, 8, "client-b")
	tab.mu.Unlock()
	assertSpans(t, "stored for client-a", forA, span(4, 8, classAnswered))
	assertSpans(t, "stored for client-b", forB, span(0, 4, classAnswered))
	// Neither query was answered headlessly.
	if written := string(channel.written()); written != "k" {
		t.Fatalf("shell input = %q, want only client-b's input", written)
	}
}

// receiveClassesSkippingOwner is receiveClassified for a view that also
// receives input-owner frames (input_owner.go), which it skips.
func receiveClassesSkippingOwner(t *testing.T, receiver *hub.Receiver, want int) ([]classSpan, []byte) {
	t.Helper()
	var spans []classSpan
	var out []byte
	for len(out) < want {
		frame := receiveTestFrame(t, receiver)
		if frame.Kind != hub.FrameJSON {
			out = append(out, frame.Data...)
			continue
		}
		var owner inputOwnerFrame
		if json.Unmarshal(frame.Data, &owner) == nil && owner.Type == inputOwnerType {
			continue
		}
		var class streamClassFrame
		if err := json.Unmarshal(frame.Data, &class); err != nil || class.Type != streamClassType {
			t.Fatalf("unexpected JSON frame %s", frame.Data)
		}
		spans = append(spans, class.classSpan)
	}
	return spans, out
}
